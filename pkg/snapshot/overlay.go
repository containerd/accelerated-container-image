/*
   Copyright The Accelerated Container Image Authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package snapshot

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/accelerated-container-image/pkg/snapshot/diskquota"

	mylog "github.com/containerd/accelerated-container-image/internal/log"
	"github.com/containerd/accelerated-container-image/pkg/metrics"
	"github.com/data-accelerator/zdfs"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/storage"
	"github.com/containerd/continuity/fs"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	"github.com/moby/locker"
)

// storageType is used to indicate that what kind of the snapshot it is.
type storageType int

const (
	// storageTypeUnknown is placeholder for unknown type.
	storageTypeUnknown storageType = iota

	// storageTypeNormal means that the unpacked layer data is from tar.gz
	// or other valid media type from OCI image spec. This kind of the
	// snapshotter can be used as normal lowerdir layer of overlayFS.
	storageTypeNormal

	// storageTypeLocalBlock means that the unpacked layer data is in
	// Overlaybd format.
	storageTypeLocalBlock

	// storageTypeRemoteBlock means that there is no unpacked layer data.
	// But there are labels to mark data that will be pulling on demand.
	storageTypeRemoteBlock

	// storageTypeLocalLayer means that the unpacked layer data is in
	// a tar file, which needs to generate overlaybd-turboOCI meta before
	// create an overlaybd device
	storageTypeLocalLayer
)

const (
	RoDir = "overlayfs" // overlayfs as rootfs. upper + lower (overlaybd)
	RwDir = "dir"       // mount overlaybd as rootfs
	RwDev = "dev"       // use overlaybd directly

	LayerBlob = "layer" // decompressed tgz layer (maybe compressed by ZFile)
)

type Registry struct {
	Host     string `json:"host"`
	Insecure bool   `json:"insecure"`
}

type BootConfig struct {
	AsyncRemove       bool                   `json:"asyncRemove"`
	Address           string                 `json:"address"`
	Root              string                 `json:"root"`
	LogLevel          string                 `json:"verbose"`
	LogReportCaller   bool                   `json:"logReportCaller"`
	RwMode            string                 `json:"rwMode"` // overlayfs, dir or dev
	AutoRemoveDev     bool                   `json:"autoRemoveDev"`
	ExporterConfig    metrics.ExporterConfig `json:"exporterConfig"`
	WritableLayerType string                 `json:"writableLayerType"` // append or sparse
	MirrorRegistry    []Registry             `json:"mirrorRegistry"`
	DefaultFsType     string                 `json:"defaultFsType"`
	RootfsQuota       string                 `json:"rootfsQuota"` // "20g" rootfs quota, only effective when rwMode is 'overlayfs'
	Tenant            int                    `json:"tenant"`      // do not set this if only a single snapshotter service in the host
	TurboFsType       []string               `json:"turboFsType"`
}

func DefaultBootConfig() *BootConfig {
	return &BootConfig{
		AsyncRemove:     false,
		LogLevel:        "info",
		RwMode:          "overlayfs",
		LogReportCaller: false,
		AutoRemoveDev:   false,
		ExporterConfig: metrics.ExporterConfig{
			Enable:    false,
			UriPrefix: "/metrics",
			Port:      9863,
		},
		MirrorRegistry:    nil,
		WritableLayerType: "append",
		DefaultFsType:     "ext4",
		RootfsQuota:       "",
		Tenant:            -1,
		TurboFsType: []string{
			"erofs",
			"ext4",
		},
	}
}

type ZFileConfig struct {
	Algorithm string `json:"algorithm"`
	BlockSize int    `json:"blockSize"`
}

// SnapshotterConfig is used to configure the snapshotter instance
type SnapshotterConfig struct {
	// OverlayBDUtilBinDir contains overlaybd-create/overlaybd-commit tools
	// to handle writable device.
	OverlayBDUtilBinDir string `toml:"overlaybd_util_bin_dir" json:"overlaybd_util_bin_dir"`
}

var defaultConfig = SnapshotterConfig{
	OverlayBDUtilBinDir: "/opt/overlaybd/bin",
}

// Opt is an option to configure the snapshotter
type Opt func(config *SnapshotterConfig) error

// AsynchronousRemove defers removal of filesystem content until
// the Cleanup method is called. Removals will make the snapshot
// referred to by the key unavailable and make the key immediately
// available for re-use.
func AsynchronousRemove(config *BootConfig) error {
	config.AsyncRemove = true
	return nil
}

// snapshotter is implementation of github.com/containerd/containerd/snapshots.Snapshotter.
//
// It is a snapshotter plugin. The layout of root dir is organized:
//
//	# snapshots stores each snapshot's data in unique folder named by auto-
//	# -incrementing integer (a.k.a Version ID).
//	#
//	# The snapshotter is based on overlayFS. It is the same with the containerd
//	# overlayFS plugin, which means that it can support normal OCI image.
//	#
//	# If the pull job doesn't support `containerd.io/snapshot.ref` and `containerd.io/snapshot/image-ref`,
//	# the snapshotter will use localBD mode to support the blob data in overlaybd
//	# format. The ${ID}/fs will be empty and the real file data will be placed
//	# in the ${ID}/block/mountpoint. It is the same to the remoteBD mode.
//	#
//	- snapshots/
//	  |_ ${ID}/
//	  |   |_ fs/               # lowerdir or upperdir
//	  |   |_ work/             # workdir
//	  |   |_ block/            # tcmu block device
//	  |      |_ config.v1.json     # config for overlaybd
//	  |      |_ init-debug.log     # shows the debug log when creating overlaybd device
//	  |      |_ mountpoint         # the block device will mount on this if the snapshot is based on overlaybd
//	  |      |_ writable_data      # exists if the block is writable in active snapshotter
//	  |      |_ writable_index     # exists if the block is writable in active snapshotter
//	  |
//	  |_ ...
//
//
//	# metadata.db is managed by github.com/containerd/containerd/snapshots/storage
//	# based on boltdb.
//	#
//	- metadata.db
type snapshotter struct {
	root              string
	rwMode            string
	config            SnapshotterConfig
	metacopyOption    string
	ms                *storage.MetaStore
	indexOff          bool
	autoRemoveDev     bool
	writableLayerType string
	mirrorRegistry    []Registry
	defaultFsType     string
	tenant            int
	locker            *locker.Locker
	turboFsType       []string
	asyncRemove       bool

	quotaDriver *diskquota.PrjQuotaDriver
	quotaSize   string
}

// NewSnapshotter returns a Snapshotter which uses block device based on overlayFS.
func NewSnapshotter(bootConfig *BootConfig, opts ...Opt) (snapshots.Snapshotter, error) {
	config := defaultConfig
	for _, opt := range opts {
		if err := opt(&config); err != nil {
			return nil, err
		}
	}

	if err := os.MkdirAll(bootConfig.Root, 0700); err != nil {
		return nil, err
	}

	ms, err := storage.NewMetaStore(filepath.Join(bootConfig.Root, "metadata.db"))
	if err != nil {
		return nil, err
	}

	if err := os.Mkdir(filepath.Join(bootConfig.Root, "snapshots"), 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}

	root, err := filepath.EvalSymlinks(bootConfig.Root)
	if err != nil {
		log.L.Errorf("invalid root: %s. (%s)", bootConfig.Root, err.Error())
		return nil, err
	}
	log.L.Infof("new snapshotter: root = %s", root)

	metacopyOption := ""
	if _, err := os.Stat("/sys/module/overlay/parameters/metacopy"); err == nil {
		metacopyOption = "metacopy=on"
	}

	// figure out whether "index=off" option is recognized by the kernel
	var indexOff bool
	if _, err = os.Stat("/sys/module/overlay/parameters/index"); err == nil {
		indexOff = true
	}

	if bootConfig.MirrorRegistry != nil {
		log.L.Infof("mirror Registry: %+v", bootConfig.MirrorRegistry)
	}

	return &snapshotter{
		root:              root,
		rwMode:            bootConfig.RwMode,
		ms:                ms,
		indexOff:          indexOff,
		config:            config,
		metacopyOption:    metacopyOption,
		autoRemoveDev:     bootConfig.AutoRemoveDev,
		writableLayerType: bootConfig.WritableLayerType,
		mirrorRegistry:    bootConfig.MirrorRegistry,
		defaultFsType:     bootConfig.DefaultFsType,
		locker:            locker.New(),
		turboFsType:       bootConfig.TurboFsType,
		tenant:            bootConfig.Tenant,
		quotaSize:         bootConfig.RootfsQuota,
		quotaDriver: &diskquota.PrjQuotaDriver{
			QuotaIDs: make(map[uint32]struct{}),
		},
		asyncRemove: bootConfig.AsyncRemove,
	}, nil
}

// Stat returns the info for an active or committed snapshot by the key.
func (o *snapshotter) Stat(ctx context.Context, key string) (_ snapshots.Info, retErr error) {
	log.G(ctx).Infof("Stat (key: %s)", key)
	start := time.Now()
	defer func() {
		if retErr != nil {
			metrics.GRPCErrCount.WithLabelValues("Stat").Inc()
		}
		metrics.GRPCLatency.WithLabelValues("Stat").Observe(time.Since(start).Seconds())
	}()
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return snapshots.Info{}, err
	}
	defer t.Rollback()

	_, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return snapshots.Info{}, err
	}
	return info, nil
}

// Updates updates the label of the given snapshot.
//
// NOTE: It supports patch-update.
//
// TODO(fuweid): should not touch the interface-like or internal label!
func (o *snapshotter) Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (_ snapshots.Info, retErr error) {
	log.G(ctx).Infof("Update (fieldpaths: %s)", fieldpaths)
	start := time.Now()
	defer func() {
		if retErr != nil {
			metrics.GRPCErrCount.WithLabelValues("Update").Inc()
		}
		metrics.GRPCLatency.WithLabelValues("Update").Observe(time.Since(start).Seconds())
	}()

	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return snapshots.Info{}, err
	}

	info, err = storage.UpdateInfo(ctx, info, fieldpaths...)
	if err != nil {
		t.Rollback()
		return snapshots.Info{}, err
	}

	if err := t.Commit(); err != nil {
		return snapshots.Info{}, err
	}
	return info, nil
}

// Usage returns the resources taken by the snapshot identified by key.
func (o *snapshotter) Usage(ctx context.Context, key string) (_ snapshots.Usage, retErr error) {
	log.G(ctx).Infof("Usage (key: %s)", key)
	start := time.Now()
	defer func() {
		if retErr != nil {
			metrics.GRPCErrCount.WithLabelValues("Usage").Inc()
		}
		metrics.GRPCLatency.WithLabelValues("Usage").Observe(time.Since(start).Seconds())
	}()

	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return snapshots.Usage{}, err
	}

	id, info, usage, err := storage.GetInfo(ctx, key)
	t.Rollback()
	if err != nil {
		return snapshots.Usage{}, err
	}

	stype, err := o.identifySnapshotStorageType(ctx, id, info)
	if err != nil {
		return snapshots.Usage{}, err
	}

	switch info.Kind {
	case snapshots.KindActive:
		return o.diskUsageWithBlock(ctx, id, stype)
	case snapshots.KindCommitted:
		if stype == storageTypeRemoteBlock || stype == storageTypeLocalBlock {
			return o.diskUsageWithBlock(ctx, id, stype)
		}
	}
	return usage, nil
}

func (o *snapshotter) getWritableType(ctx context.Context, id string, info snapshots.Info) (mode string) {
	defer func() {
		log.G(ctx).Infof("snapshot R/W label: %s", mode)
	}()
	// check image type (OCIv1 or overlaybd)
	if id != "" {
		if _, err := o.loadBackingStoreConfig(id); err != nil {
			log.G(ctx).Debugf("[%s] is not an overlaybd image.", id)
			return RoDir
		}
	} else {
		log.G(ctx).Debugf("empty snID get. It should be an initial layer.")
	}
	// overlaybd
	rwMode := func(m string) string {
		if m == "dir" {
			return RwDir
		}
		if m == "dev" {
			return RwDev
		}
		return RoDir
	}
	m, ok := info.Labels[label.SupportReadWriteMode]
	if !ok {
		return rwMode(o.rwMode)
	}

	return rwMode(m)
}

func (o *snapshotter) checkTurboOCI(labels map[string]string) (bool, string, string) {
	if st1, ok1 := labels[label.TurboOCIDigest]; ok1 {
		return true, st1, labels[label.TurboOCIMediaType]
	}

	if st2, ok2 := labels[label.FastOCIDigest]; ok2 {
		return true, st2, labels[label.FastOCIMediaType]
	}
	return false, "", ""
}

func (o *snapshotter) isPrepareRootfs(info snapshots.Info) bool {
	if _, ok := info.Labels[label.TargetSnapshotRef]; ok {
		return false
	}
	if info.Labels[label.SnapshotType] == "image" {
		return false
	}
	return true
}

func (o *snapshotter) createMountPoint(ctx context.Context, kind snapshots.Kind, key string, parent string, opts ...snapshots.Opt) (_ []mount.Mount, retErr error) {

	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return nil, err
	}
	rollback := true

	defer func() {
		if retErr != nil && rollback {
			if rerr := t.Rollback(); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
			}
		}
	}()

	id, info, err := o.createSnapshot(ctx, kind, key, parent, opts)
	if err != nil {
		return nil, err
	}
	log.G(ctx).Infof("sn: %s, labels:[%+v]", id, info.Labels)
	defer func() {
		// the transaction rollback makes created snapshot invalid, just clean it.
		if retErr != nil && rollback {
			if rerr := os.RemoveAll(o.snPath(id)); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to cleanup")
			}
		}
	}()

	s, err := storage.GetSnapshot(ctx, key)
	if err != nil {
		return nil, err
	}

	var (
		parentID   string
		parentInfo snapshots.Info
	)

	if parent != "" {
		parentID, parentInfo, _, err = storage.GetInfo(ctx, parent)
		if err != nil {
			return nil, fmt.Errorf("failed to get info of parent snapshot %s: %w", parent, err)
		}
	}

	// If the layer is in overlaybd format, construct backstore spec and saved it into snapshot dir.
	// Return ErrAlreadyExists to skip pulling and unpacking layer. See https://github.com/containerd/containerd/blob/master/docs/remote-snapshotter.md#snapshotter-apis-for-querying-remote-snapshots
	// This part code is only for Pull.
	if targetRef, ok := info.Labels[label.TargetSnapshotRef]; ok {

		// Acceleration layer will not use remote snapshot. It still needs containerd to pull,
		// unpack blob and commit snapshot. So return a normal mount point here.
		isAccelLayer := info.Labels[label.AccelerationLayer]
		log.G(ctx).Debugf("Prepare (targetRefLabel: %s, accelLayerLabel: %s)", targetRef, isAccelLayer)
		if isAccelLayer == "yes" {
			if err := o.constructSpecForAccelLayer(id, parentID); err != nil {
				return nil, fmt.Errorf("constructSpecForAccelLayer failed: id %s: %w", id, err)
			}
			rollback = false
			if err := t.Commit(); err != nil {
				return nil, err
			}
			return []mount.Mount{{
				Source: o.upperPath(id),
				Type:   "bind",
				Options: []string{
					"rw",
					"bind",
				}},
			}, nil
		}

		stype, err := o.identifySnapshotStorageType(ctx, id, info)
		log.G(ctx).Debugf("Prepare (storageType: %d)", stype)
		if err != nil {
			return nil, err
		}

		// Download blob
		downloadBlob := info.Labels[label.DownloadRemoteBlob]
		if downloadBlob == "download" && stype == storageTypeRemoteBlock {
			log.G(ctx).Infof("download blob %s", targetRef)
			if err := o.constructOverlayBDSpec(ctx, key, false); err != nil {
				return nil, err
			}
			rollback = false
			if err := t.Commit(); err != nil {
				return nil, err
			}
			return []mount.Mount{{
				Source: o.upperPath(id),
				Type:   "bind",
				Options: []string{
					"rw",
					"bind",
				}},
			}, nil
		}

		// Normal prepare for TurboOCI
		if isTurboOCI, digest, _ := o.checkTurboOCI(info.Labels); isTurboOCI {
			log.G(ctx).Infof("%s is turboOCI.v1 layer: %s", s.ID, digest)
			stype = storageTypeNormal
		}
		if stype == storageTypeRemoteBlock {
			info.Labels[label.RemoteLabel] = label.RemoteLabelVal // Mark this snapshot as remote
			if _, _, err = o.commit(ctx, targetRef, key,
				append(opts, snapshots.WithLabels(info.Labels))...); err != nil {
				return nil, err
			}

			if err := o.constructOverlayBDSpec(ctx, targetRef, false); err != nil {
				return nil, err
			}

			rollback = false
			if err := t.Commit(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("target snapshot %q: %w", targetRef, errdefs.ErrAlreadyExists)
		}
	}

	stype := storageTypeNormal
	writeType := o.getWritableType(ctx, parentID, info)

	// If Preparing for rootfs, find metadata from its parent (top layer), launch and mount backstore device.
	if o.isPrepareRootfs(info) {

		log.G(ctx).Infof("Preparing rootfs(%s). writeType: %s", s.ID, writeType)
		if writeType != RoDir {
			stype = storageTypeLocalBlock
			if err := o.constructOverlayBDSpec(ctx, key, true); err != nil {
				log.G(ctx).Errorln(err.Error())
				return nil, err
			}
		} else if parent != "" {
			stype, err = o.identifySnapshotStorageType(ctx, parentID, parentInfo)
			if err != nil {
				return nil, err
			}
		}
		switch stype {
		case storageTypeLocalBlock, storageTypeRemoteBlock:
			if parent != "" {
				parentIsAccelLayer := parentInfo.Labels[label.AccelerationLayer] == "yes"
				needRecordTrace := info.Labels[label.RecordTrace] == "yes"
				recordTracePath := info.Labels[label.RecordTracePath]
				log.G(ctx).Infof("Prepare rootfs (sn: %s, parentIsAccelLayer: %t, needRecordTrace: %t, recordTracePath: %s)",
					id, parentIsAccelLayer, needRecordTrace, recordTracePath)

				if parentIsAccelLayer {
					log.G(ctx).Infof("get accel-layer in parent (id: %s)", id)
					// If parent is already an acceleration layer, there is certainly no need to record trace.
					// Just mark this layer to get accelerated (trace replay)
					err = o.updateSpec(parentID, true, "")
					if writeType != RoDir {
						o.updateSpec(id, true, "")
					}
				} else if needRecordTrace && recordTracePath != "" {
					err = o.updateSpec(parentID, false, recordTracePath)
					if writeType != RoDir {
						o.updateSpec(id, false, recordTracePath)
					}
				} else {
					// For the compatibility of images which have no accel layer
					err = o.updateSpec(parentID, false, "")
				}
				if err != nil {
					return nil, fmt.Errorf("updateSpec failed for snapshot %s: %w", parentID, err)
				}
			}

			var obdID string
			var obdInfo *snapshots.Info
			if writeType != RoDir {
				obdID = id
				obdInfo = &info
			} else {
				obdID = parentID
				obdInfo = &parentInfo
			}
			fsType, ok := obdInfo.Labels[label.OverlayBDBlobFsType]
			if !ok {
				if isTurboOCI, _, _ := o.checkTurboOCI(obdInfo.Labels); isTurboOCI {
					_, fsType = o.turboOCIFsMeta(obdID)
				} else {
					log.G(ctx).Warnf("cannot get fs type from label, %v, using %s", obdInfo.Labels, fsType)
					fsType = o.defaultFsType
				}
			}
			log.G(ctx).Debugf("attachAndMountBlockDevice (obdID: %s, writeType: %s, fsType %s, targetPath: %s)",
				obdID, writeType, fsType, o.overlaybdTargetPath(obdID))
			if err = o.attachAndMountBlockDevice(ctx, obdID, writeType, fsType, parent == ""); err != nil {
				log.G(ctx).Errorf("%v", err)
				return nil, fmt.Errorf("failed to attach and mount for snapshot %v: %w", obdID, err)
			}

			defer func() {
				if retErr != nil && writeType == RwDir { // It's unnecessary to umount overlay block device if writeType == writeTypeRawDev
					if rerr := mount.Unmount(o.overlaybdMountpoint(obdID), 0); rerr != nil {
						log.G(ctx).WithError(rerr).Warnf("failed to umount writable block %s", o.overlaybdMountpoint(obdID))
					}
				}
			}()
		default:
			// do nothing
		}
	}
	if _, writableBD := info.Labels[label.SupportReadWriteMode]; stype == storageTypeNormal && writableBD {
		// if is not overlaybd writable layer, delete label before commit
		delete(info.Labels, label.SupportReadWriteMode)
		storage.UpdateInfo(ctx, info)
	}

	rollback = false
	if err := t.Commit(); err != nil {
		return nil, err
	}

	var m []mount.Mount
	switch stype {
	case storageTypeNormal:
		if _, ok := info.Labels[label.LayerToTurboOCI]; ok {
			m = []mount.Mount{
				{
					Source: o.upperPath(s.ID),
					Type:   "bind",
					Options: []string{
						"rw",
						"rbind",
					},
				},
			}
		} else {
			m = o.normalOverlayMount(s)
		}
	case storageTypeLocalBlock, storageTypeRemoteBlock:
		m, err = o.basedOnBlockDeviceMount(ctx, s, writeType)
		if err != nil {
			return nil, err
		}
	default:
		panic("unreachable")
	}
	log.G(ctx).Debugf("return mount point(R/W mode: %s): %v", writeType, m)
	return m, nil

}

// Prepare creates an active snapshot identified by key descending from the provided parent.
func (o *snapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) (_ []mount.Mount, retErr error) {
	log.G(ctx).Infof("Prepare (key: %s, parent: %s)", key, parent)
	start := time.Now()
	defer func() {
		if retErr != nil {
			metrics.GRPCErrCount.WithLabelValues("Prepare").Inc()
		} else {
			log.G(ctx).WithFields(log.Fields{
				"d":      time.Since(start),
				"key":    key,
				"parent": parent,
			}).Infof("Prepare")
		}
		metrics.GRPCLatency.WithLabelValues("Prepare").Observe(time.Since(start).Seconds())

	}()
	return o.createMountPoint(ctx, snapshots.KindActive, key, parent, opts...)
}

// View returns a readonly view on parent snapshotter.
func (o *snapshotter) View(ctx context.Context, key, parent string, opts ...snapshots.Opt) (_ []mount.Mount, retErr error) {
	log.G(ctx).Infof("View (key: %s, parent: %s)", key, parent)
	defer log.G(ctx).Debugf("return View (key: %s, parent: %s)", key, parent)
	start := time.Now()
	defer func() {
		if retErr != nil {
			metrics.GRPCErrCount.WithLabelValues("View").Inc()
		}
		metrics.GRPCLatency.WithLabelValues("View").Observe(time.Since(start).Seconds())
	}()
	return o.createMountPoint(ctx, snapshots.KindView, key, parent, opts...)
}

// Mounts returns the mounts for the transaction identified by key. Can be
// called on an read-write or readonly transaction.
//
// This can be used to recover mounts after calling View or Prepare.
func (o *snapshotter) Mounts(ctx context.Context, key string) (_ []mount.Mount, retErr error) {
	log.G(ctx).Infof("Mounts (key: %s)", key)

	start := time.Now()
	defer func() {
		if retErr != nil {
			metrics.GRPCErrCount.WithLabelValues("Mounts").Inc()
		} else {
			log.G(ctx).WithFields(log.Fields{
				"d":   time.Since(start),
				"key": key,
			}).Infof("Mounts")
		}
		metrics.GRPCLatency.WithLabelValues("Mounts").Observe(time.Since(start).Seconds())
	}()
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return nil, err
	}
	defer t.Rollback()

	s, err := storage.GetSnapshot(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get active mount: %w", err)
	}

	log.G(ctx).Debugf("Mounts (key: %s, id: %s, parentID: %s, kind: %d)", key, s.ID, s.ParentIDs, s.Kind)

	if len(s.ParentIDs) > 0 {
		if o.autoRemoveDev {
			o.locker.Lock(s.ID)
			defer o.locker.Unlock(s.ID)
			o.locker.Lock(s.ParentIDs[0])
			defer o.locker.Unlock(s.ParentIDs[0])
		}

		_, info, _, err := storage.GetInfo(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("failed to get info: %w", err)
		}

		writeType := o.getWritableType(ctx, s.ID, info)
		if writeType != RoDir {
			return o.basedOnBlockDeviceMount(ctx, s, writeType)
		}

		parentID, parentInfo, _, err := storage.GetInfo(ctx, info.Parent)
		if err != nil {
			return nil, fmt.Errorf("failed to get info of parent snapshot %s: %w", info.Parent, err)
		}

		parentStype, err := o.identifySnapshotStorageType(ctx, parentID, parentInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to identify storage of parent snapshot %s: %w", parentInfo.Name, err)
		}

		if parentStype == storageTypeRemoteBlock || parentStype == storageTypeLocalBlock {
			fsType, ok := parentInfo.Labels[label.OverlayBDBlobFsType]
			if !ok {
				if isTurboOCI, _, _ := o.checkTurboOCI(parentInfo.Labels); isTurboOCI {
					_, fsType = o.turboOCIFsMeta(parentID)
				} else {
					fsType = o.defaultFsType
				}
			}
			if err := o.attachAndMountBlockDevice(ctx, parentID, RoDir, fsType, false); err != nil {
				return nil, fmt.Errorf("failed to attach and mount for snapshot %v: %w", key, err)
			}
			return o.basedOnBlockDeviceMount(ctx, s, RoDir)
		}

	}
	return o.normalOverlayMount(s), nil
}

// Commit
func (o *snapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) (retErr error) {
	log.G(ctx).Infof("Commit (key: %s, name: %s)", key, name)
	start := time.Now()
	defer func() {
		if retErr != nil {
			metrics.GRPCErrCount.WithLabelValues("Commit").Inc()
		} else {
			log.G(ctx).WithFields(log.Fields{
				"d":    time.Since(start),
				"name": name,
				"key":  key,
			}).Infof("Commit")
		}
		metrics.GRPCLatency.WithLabelValues("Commit").Observe(time.Since(start).Seconds())
	}()

	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return err
	}

	rollback := true
	defer func() {
		if retErr != nil && rollback {
			if rerr := t.Rollback(); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
			}
		}

	}()

	id, oinfo, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to get info of snapshot %s: %w", key, err)
	}

	// if writable, should commit the data and make it immutable.
	if _, writableBD := oinfo.Labels[label.SupportReadWriteMode]; writableBD {
		// TODO(fuweid): how to rollback?
		if oinfo.Labels[label.AccelerationLayer] == "yes" {
			log.G(ctx).Info("Commit accel-layer requires no writable_data")
		} else if _, err := o.loadBackingStoreConfig(id); err != nil {
			log.G(ctx).Info("not an overlaybd writable layer")
		} else {
			if err := o.unmountAndDetachBlockDevice(ctx, id, key); err != nil {
				return fmt.Errorf("failed to destroy target device for snapshot %s: %w", key, err)
			}

			if err := o.sealWritableOverlaybd(ctx, id); err != nil {
				return err
			}

			opts = append(opts, snapshots.WithLabels(map[string]string{label.LocalOverlayBDPath: o.overlaybdSealedFilePath(id)}))
		}
	}

	if isOverlaybd, err := zdfs.PrepareOverlayBDSpec(ctx, key, id, o.snPath(id), oinfo, o.snPath); isOverlaybd {
		log.G(ctx).Infof("sn: %s is an overlaybd layer", id)
	} else if err != nil {
		log.G(ctx).Errorf("prepare overlaybd spec failed(sn: %s): %s", id, err.Error())
		return err
	}

	id, info, err := o.commit(ctx, name, key, opts...)
	if err != nil {
		return err
	}

	stype, err := o.identifySnapshotStorageType(ctx, id, info)
	if err != nil {
		return err
	}

	log.G(ctx).Debugf("Commit info (id: %s, info: %v, stype: %d)", id, info.Labels, stype)

	// For turboOCI, we need to construct OverlayBD spec after unpacking
	// since there could be multiple fs metadata in a turboOCI layer
	if isTurboOCI, digest, _ := o.checkTurboOCI(info.Labels); isTurboOCI {
		log.G(ctx).Infof("commit turboOCI.v1 layer: (%s, %s)", id, digest)
		if err := o.constructOverlayBDSpec(ctx, name, false); err != nil {
			return fmt.Errorf("failed to construct overlaybd config: %w", err)
		}
		stype = storageTypeNormal
	}

	// Firstly, try to convert an OCIv1 tarball to a turboOCI layer.
	// then change stype to 'storageTypeLocalBlock' which can make it attach a overlaybd block
	if stype == storageTypeLocalLayer {
		log.G(ctx).Infof("convet a local blob to turboOCI layer (sn: %s)", id)
		if err := o.constructOverlayBDSpec(ctx, name, false); err != nil {
			return fmt.Errorf("failed to construct overlaybd config: %w", err)
		}
		stype = storageTypeLocalBlock
	}

	if stype == storageTypeLocalBlock {
		if err := o.constructOverlayBDSpec(ctx, name, false); err != nil {
			return fmt.Errorf("failed to construct overlaybd config: %w", err)
		}

		if info.Labels == nil {
			info.Labels = make(map[string]string)
		}

		info.Labels[label.LocalOverlayBDPath] = o.overlaybdSealedFilePath(id)
		delete(info.Labels, label.SupportReadWriteMode)
		info, err = storage.UpdateInfo(ctx, info)
		if err != nil {
			return err
		}

		log.G(ctx).Debugf("Commit info (info: %v)", info.Labels)
	}

	rollback = false
	return t.Commit()
}

func (o *snapshotter) commit(ctx context.Context, name, key string, opts ...snapshots.Opt) (string, snapshots.Info, error) {
	id, _, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return "", snapshots.Info{}, err
	}

	usage, err := fs.DiskUsage(ctx, o.upperPath(id))
	if err != nil {
		return "", snapshots.Info{}, err
	}

	if _, err := storage.CommitActive(ctx, key, name, snapshots.Usage(usage), opts...); err != nil {
		return "", snapshots.Info{}, fmt.Errorf("failed to commit snapshot: %w", err)
	}

	id, info, _, err := storage.GetInfo(ctx, name)
	if err != nil {
		return "", snapshots.Info{}, err
	}
	return id, info, nil
}

func (o *snapshotter) cleanupDirectories(ctx context.Context) ([]string, error) {
	// Get a write transaction to ensure no other write transaction can be entered
	// while the cleanup is scanning.
	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return nil, err
	}

	defer t.Rollback()
	return o.getCleanupDirectories(ctx, t)
}
func (o *snapshotter) getCleanupDirectories(ctx context.Context, t storage.Transactor) ([]string, error) {
	ids, err := storage.IDMap(ctx)
	if err != nil {
		return nil, err
	}

	snapshotDir := filepath.Join(o.root, "snapshots")
	fd, err := os.Open(snapshotDir)
	if err != nil {
		return nil, err
	}
	defer fd.Close()

	dirs, err := fd.Readdirnames(0)
	if err != nil {
		return nil, err
	}

	cleanup := []string{}
	for _, d := range dirs {
		if _, ok := ids[d]; ok {
			continue
		}

		cleanup = append(cleanup, filepath.Join(snapshotDir, d))
	}

	return cleanup, nil
}

// Cleanup cleans up disk resources from removed or abandoned snapshots// Cleanup cleans up disk resources from removed or abandoned snapshots
func (o *snapshotter) Cleanup(ctx context.Context) error {
	cleanup, err := o.cleanupDirectories(ctx)
	if err != nil {
		return err
	}

	for _, dir := range cleanup {
		if err := os.RemoveAll(dir); err != nil {
			log.G(ctx).WithError(err).WithField("path", dir).Warn("failed to remove directory")
		}
	}

	return nil
}

// Remove abandons the snapshot identified by key. The snapshot will
// immediately become unavailable and unrecoverable.
func (o *snapshotter) Remove(ctx context.Context, key string) (err error) {
	log.G(ctx).Infof("Remove (key: %s)", key)
	start := time.Now()
	defer func() {
		if err != nil {
			metrics.GRPCErrCount.WithLabelValues("Remove").Inc()
		}
		metrics.GRPCLatency.WithLabelValues("Remove").Observe(time.Since(start).Seconds())
	}()

	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return err
	}

	rollback := true
	defer func() {
		if rollback {
			if rerr := t.Rollback(); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
			}
		} else {
			t.Commit()
		}
	}()

	id, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return err
	}

	stype, err := o.identifySnapshotStorageType(ctx, id, info)
	if err != nil {
		return err
	}

	if stype != storageTypeNormal {
		_, err = os.Stat(o.overlaybdBackstoreMarkFile(id))
		if err == nil {
			err = o.unmountAndDetachBlockDevice(ctx, id, key)
			if err != nil {
				return mylog.TracedErrorf(ctx, "failed to destroy target device for snapshot %s: %w", key, err)
			}
		}
	}

	// for TypeNormal, verify its(parent) meets the condition of overlaybd format
	if o.autoRemoveDev {
		if s, err := storage.GetSnapshot(ctx, key); err == nil && s.Kind == snapshots.KindActive && len(s.ParentIDs) > 0 {
			o.locker.Lock(s.ID)
			defer o.locker.Unlock(s.ID)
			o.locker.Lock(s.ParentIDs[0])
			defer o.locker.Unlock(s.ParentIDs[0])

			log.G(ctx).Debugf("try to verify parent snapshots format.")
			if st, err := o.identifySnapshotStorageType(ctx, s.ParentIDs[0], info); err == nil && st != storageTypeNormal {
				err = o.unmountAndDetachBlockDevice(ctx, s.ParentIDs[0], "")
				if err != nil {
					return mylog.TracedErrorf(ctx, "failed to destroy target device for snapshot %s: %w", key, err)
				}
			}
		}
	}

	// Just in case, check if snapshot contains mountpoint
	mounted, err := o.isMounted(ctx, o.overlaybdMountpoint(id))
	if err != nil {
		return mylog.TracedErrorf(ctx, "failed to check mountpoint: %w", err)
	} else if mounted {
		return mylog.TracedErrorf(ctx, "try to remove snapshot %s which still have mountpoint", id)
	}

	_, _, err = storage.Remove(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to remove: %w", err)
	}
	if !o.asyncRemove {
		var removals []string
		removals, err = o.getCleanupDirectories(ctx, t)
		if err != nil {
			return fmt.Errorf("unable to get directories for removal: %w", err)
		}
		defer func() {
			if err == nil {
				for _, dir := range removals {
					if err := os.RemoveAll(dir); err != nil {
						log.G(ctx).WithError(err).WithField("path", dir).Warn("failed to remove directory")
					}
				}
			}
		}()
	} else {
		log.G(ctx).Info("asyncRemove enabled, remove snapshots in Cleanup() method.")
	}

	rollback = false
	return nil
}

// Walk the snapshots.
func (o *snapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, fs ...string) (err error) {
	log.G(ctx).Infof("Walk (fs: %s)", fs)
	start := time.Now()
	defer func() {
		if err != nil {
			metrics.GRPCErrCount.WithLabelValues("Walk").Inc()
		}
		metrics.GRPCLatency.WithLabelValues("Walk").Observe(time.Since(start).Seconds())
	}()

	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return err
	}
	defer t.Rollback()
	return storage.WalkInfo(ctx, fn, fs...)
}

func (o *snapshotter) prepareDirectory(ctx context.Context, snapshotDir string, kind snapshots.Kind) (string, error) {
	td, err := os.MkdirTemp(snapshotDir, "new-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err := os.Mkdir(filepath.Join(td, "fs"), 0755); err != nil {
		return td, err
	}
	if err := os.Mkdir(filepath.Join(td, "block"), 0711); err != nil {
		return td, err
	}
	if err := os.Mkdir(filepath.Join(td, "block", "mountpoint"), 0711); err != nil {
		return td, err
	}

	f, err := os.Create(filepath.Join(td, "block", "init-debug.log"))
	f.Close()
	if err != nil {
		return td, err
	}

	if kind == snapshots.KindActive {
		if err := os.Mkdir(filepath.Join(td, "work"), 0711); err != nil {
			return td, err
		}
	}
	return td, nil
}

func (o *snapshotter) basedOnBlockDeviceMount(ctx context.Context, s storage.Snapshot, writeType string) (m []mount.Mount, err error) {
	defer func() {
		if err == nil {
			log.G(ctx).Infof("return mount point(R/W mode: %s): %v", writeType, m)
		} else {
			log.G(ctx).Errorf("basedOnBlockDeviceMount return error: %v", err)
		}
	}()
	rwflag := "rw"
	if s.Kind == snapshots.KindView && (writeType == rwflag || writeType == RwDev) {
		log.G(ctx).Infof("snapshot.Kind == View, reset overlaybd as READ-ONLY.")
		rwflag = "ro"
	}
	if writeType == RwDir {
		return []mount.Mount{
			{
				Source: o.overlaybdMountpoint(s.ID),
				Type:   "bind",
				Options: []string{
					rwflag,
					"rbind",
				},
			},
		}, nil
	}
	if writeType == RwDev {
		devName, err := os.ReadFile(o.overlaybdBackstoreMarkFile(s.ID))
		if err != nil {
			return nil, err
		}
		// TODO: support multi fs
		return []mount.Mount{
			{
				Source: string(devName),
				Type:   "ext4",
				Options: []string{
					rwflag,
					"discard",
				},
			},
		}, nil
	}

	if len(s.ParentIDs) >= 1 {
		// for ro mode, always has parent
		if s.Kind == snapshots.KindActive {
			var options []string
			if o.indexOff {
				options = append(options, "index=off")
			}
			options = append(options,
				fmt.Sprintf("workdir=%s", o.workPath(s.ID)),
				fmt.Sprintf("upperdir=%s", o.upperPath(s.ID)),
				fmt.Sprintf("lowerdir=%s", o.overlaybdMountpoint(s.ParentIDs[0])),
			)
			return []mount.Mount{
				{
					Type:    "overlay",
					Source:  "overlay",
					Options: options,
				},
			}, nil
		}
		return []mount.Mount{
			{
				Source: o.overlaybdMountpoint(s.ParentIDs[0]),
				Type:   "bind",
				Options: []string{
					"ro",
					"rbind",
				},
			},
		}, nil
	}

	// ro and no parent, will not work
	return []mount.Mount{
		{
			Source: o.overlaybdMountpoint(s.ID),
			Type:   "bind",
			Options: []string{
				"ro",
				"rbind",
			},
		},
	}, nil
}

func (o *snapshotter) normalOverlayMount(s storage.Snapshot) []mount.Mount {
	if len(s.ParentIDs) == 0 {
		roFlag := "rw"
		if s.Kind == snapshots.KindView {
			roFlag = "ro"
		}

		return []mount.Mount{
			{
				Source: o.upperPath(s.ID),
				Type:   "bind",
				Options: []string{
					roFlag,
					"rbind",
				},
			},
		}
	}
	var options []string

	if o.indexOff {
		options = append(options, "index=off")
	}

	if s.Kind == snapshots.KindActive {
		options = append(options,
			fmt.Sprintf("workdir=%s", o.workPath(s.ID)),
			fmt.Sprintf("upperdir=%s", o.upperPath(s.ID)),
		)
		// if o.metacopyOption != "" {
		// 	options = append(options, o.metacopyOption)
		// }
	} else if len(s.ParentIDs) == 1 {
		return []mount.Mount{
			{
				Source: o.upperPath(s.ParentIDs[0]),
				Type:   "bind",
				Options: []string{
					"ro",
					"rbind",
				},
			},
		}
	}

	parentPaths := make([]string, len(s.ParentIDs))
	for i := range s.ParentIDs {
		parentPaths[i] = o.upperPath(s.ParentIDs[i])
	}

	options = append(options, fmt.Sprintf("lowerdir=%s", strings.Join(parentPaths, ":")))
	return []mount.Mount{
		{
			Type:    "overlay",
			Source:  "overlay",
			Options: options,
		},
	}
}

func (o *snapshotter) getDiskQuotaSize(info *snapshots.Info) string {
	if info.Labels != nil {
		if size, ok := info.Labels[label.RootfsQuotaLabel]; ok {
			return size
		}
	}
	return o.quotaSize
}

func (o *snapshotter) createSnapshot(ctx context.Context, kind snapshots.Kind, key, parent string, opts []snapshots.Opt) (_ string, _ snapshots.Info, err error) {
	var td, path string
	defer func() {
		if err != nil {
			if td != "" {
				if err1 := os.RemoveAll(td); err1 != nil {
					log.G(ctx).WithError(err1).Warn("failed to cleanup temp snapshot directory")
				}
			}
			if path != "" {
				if err1 := os.RemoveAll(path); err1 != nil {
					log.G(ctx).WithError(err1).WithField("path", path).Error("failed to reclaim snapshot directory, directory may need removal")
					err = fmt.Errorf("failed to remove path: %v: %w", err1, err)
				}
			}
		}
	}()

	snapshotDir := filepath.Join(o.root, "snapshots")

	td, err = o.prepareDirectory(ctx, snapshotDir, kind)
	if err != nil {
		return "", snapshots.Info{}, fmt.Errorf("failed to create prepare snapshot dir: %w", err)
	}

	s, err := storage.CreateSnapshot(ctx, kind, key, parent, opts...)
	if err != nil {
		return "", snapshots.Info{}, fmt.Errorf("failed to create snapshot: %w", err)
	}

	if len(s.ParentIDs) > 0 {
		st, err := os.Stat(o.upperPath(s.ParentIDs[0]))
		if err != nil {
			return "", snapshots.Info{}, fmt.Errorf("failed to stat parent: %w", err)
		}

		stat := st.Sys().(*syscall.Stat_t)
		if err := os.Lchown(filepath.Join(td, "fs"), int(stat.Uid), int(stat.Gid)); err != nil {
			return "", snapshots.Info{}, fmt.Errorf("failed to chown: %w", err)
		}
	}
	// _, tmpinfo, _, err := storage.GetInfo(ctx, key)
	id, info, _, err := storage.GetInfo(ctx, key)
	if o.isPrepareRootfs(info) {
		if diskQuotaSize := o.getDiskQuotaSize(&info); diskQuotaSize != "" {
			log.G(ctx).Infof("set usage quota %s for rootfs(sn: %s)", diskQuotaSize, s.ID)
			upperPath := filepath.Join(td, "fs")
			if err := o.setDiskQuota(ctx, upperPath, diskQuotaSize, diskquota.QuotaMinID); err != nil {
				return "", snapshots.Info{}, fmt.Errorf("failed to set diskquota on upperpath, snapshot id: %s: %w", s.ID, err)
			}
			// if there's no parent, we just return a bind mount, so no need to set quota on workerpath
			if len(s.ParentIDs) > 0 {
				workpath := filepath.Join(td, "work")
				if err := o.setDiskQuota(ctx, workpath, diskQuotaSize, diskquota.QuotaMinID); err != nil {
					return "", snapshots.Info{}, fmt.Errorf("failed to set diskquota on workpath, snapshot id: %s: %w", s.ID, err)
				}
			}
		}
	}
	path = filepath.Join(snapshotDir, s.ID)
	if err = os.Rename(td, path); err != nil {
		return "", snapshots.Info{}, fmt.Errorf("failed to rename: %w", err)
	}
	td = ""
	// id, info, _, err := storage.GetInfo(ctx, key)

	if err != nil {
		return "", snapshots.Info{}, fmt.Errorf("failed to get snapshot info: %w", err)
	}
	img, ok := info.Labels[label.CRIImageRef]
	if !ok {
		img, ok = info.Labels[label.TargetImageRef]
	}
	if ok {
		log.G(ctx).Infof("found imageRef: %s", img)
		if err := os.WriteFile(filepath.Join(path, "image_ref"), []byte(img), 0644); err != nil {
			log.G(ctx).Errorf("write imageRef '%s'. path: %s, err: %v", img, filepath.Join(path, "image_ref"), err)
		}
	} else {
		log.G(ctx).Warnf("imageRef meta not found.")
	}
	return id, info, nil
}

func (o *snapshotter) identifySnapshotStorageType(ctx context.Context, id string, info snapshots.Info) (storageType, error) {
	if _, ok := info.Labels[label.TargetSnapshotRef]; ok {
		_, hasBDBlobSize := info.Labels[label.OverlayBDBlobSize]
		_, hasBDBlobDigest := info.Labels[label.OverlayBDBlobDigest]
		_, hasRef := info.Labels[label.TargetImageRef]
		_, hasCriRef := info.Labels[label.CRIImageRef]

		if hasBDBlobSize && hasBDBlobDigest {
			if hasRef || hasCriRef {
				return storageTypeRemoteBlock, nil
			}
		}

	}

	// check overlaybd.commit
	filePath := o.magicFilePath(id)
	st, err := o.identifyLocalStorageType(filePath)
	if err == nil {
		return st, nil
	}
	log.G(ctx).Debugf("failed to identify by %s, error %v, try to identify by writable_data", filePath, err)

	// check sealed data file
	filePath = o.overlaybdSealedFilePath(id)
	st, err = o.identifyLocalStorageType(filePath)
	if err == nil {
		return st, nil
	}

	// check writable data file
	filePath = o.overlaybdWritableDataPath(id)
	st, err = o.identifyLocalStorageType(filePath)
	if err == nil {
		return st, nil
	}

	// check layer.tar if it should be converted to turboOCI
	filePath = o.overlaybdOCILayerPath(id)
	if _, err := os.Stat(filePath); err == nil {
		log.G(ctx).Infof("uncompressed layer found in sn: %s", id)
		return storageTypeLocalLayer, nil
	}
	if os.IsNotExist(err) {
		// check config.v1.json
		log.G(ctx).Debugf("failed to identify by writable_data(sn: %s), try to identify by config.v1.json", id)
		filePath = o.overlaybdConfPath(id)
		if _, err := os.Stat(filePath); err == nil {
			log.G(ctx).Debugf("%s/config.v1.json found, return storageTypeRemoteBlock", id)
			return storageTypeRemoteBlock, nil
		}
		return storageTypeNormal, nil
	}

	log.G(ctx).Debugf("storageType(sn: %s): %d", id, st)

	return st, err
}

// SetDiskQuota is used to set quota for directory.
func (o *snapshotter) setDiskQuota(ctx context.Context, dir string, size string, quotaID uint32) error {
	log.G(ctx).Infof("setDiskQuota: dir %s, size %s", dir, size)
	if isRegular, err := diskquota.CheckRegularFile(dir); err != nil || !isRegular {
		log.G(ctx).Errorf("set quota skip not regular file: %s", dir)
		return err
	}

	id := o.quotaDriver.GetQuotaIDInFileAttr(dir)
	if id > 0 && id != quotaID {
		return fmt.Errorf("quota id is already set, quota id: %d", id)
	}

	log.G(ctx).Infof("try to set disk quota, dir(%s), size(%s), quotaID(%d)", dir, size, quotaID)

	if err := o.quotaDriver.SetDiskQuota(dir, size, quotaID); err != nil {
		return fmt.Errorf("failed to set dir(%s) disk quota: %w", dir, err)
	}
	return nil
}

func (o *snapshotter) snPath(id string) string {
	return filepath.Join(o.root, "snapshots", id)
}

func (o *snapshotter) upperPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "fs")
}

func (o *snapshotter) workPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "work")
}

func (o *snapshotter) convertTempdir(id string) string {
	return filepath.Join(o.root, "snapshots", id, "temp")
}

func (o *snapshotter) blockPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "block")
}

var erofsSupported = false
var erofsSupportedOnce sync.Once

// If EROFS fsmeta exists and is prioritized, check and modprobe erofs
func IsErofsSupported() bool {
	erofsSupportedOnce.Do(func() {
		fs, err := os.ReadFile("/proc/filesystems")
		if err != nil || !bytes.Contains(fs, []byte("\terofs\n")) {
			// Try to `modprobe erofs` again
			cmd := exec.Command("modprobe", "erofs")
			_, err = cmd.CombinedOutput()
			if err != nil {
				return
			}
			fs, err = os.ReadFile("/proc/filesystems")
			if err != nil || !bytes.Contains(fs, []byte("\terofs\n")) {
				return
			}
		}
		erofsSupported = true
	})
	return erofsSupported
}

func (o *snapshotter) turboOCIFsMeta(id string) (string, string) {
	for _, fsType := range o.turboFsType {
		fsmeta := filepath.Join(o.root, "snapshots", id, "fs", fsType+".fs.meta")
		if _, err := os.Stat(fsmeta); err == nil {
			if fsType == "erofs" && !IsErofsSupported() {
				log.L.Warn("erofs is not supported on this system, fallback to other fs type")
				continue
			}
			return fsmeta, fsType
		} else if !errors.Is(err, os.ErrNotExist) {
			log.L.Errorf("error while checking fs meta file: %s", err)
		}
	}
	log.L.Warn("no fs meta file found, fallback to ext4")
	return filepath.Join(o.root, "snapshots", id, "fs", "ext4.fs.meta"), "ext4"
}

func (o *snapshotter) magicFilePath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "fs", "overlaybd.commit")
}

func (o *snapshotter) overlaybdSealedFilePath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "fs", "overlaybd.sealed")
}

func (o *snapshotter) overlaybdMountpoint(id string) string {
	return filepath.Join(o.root, "snapshots", id, "block", "mountpoint")
}

func (o *snapshotter) overlaybdConfPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "block", "config.v1.json")
}

func (o *snapshotter) overlaybdInitDebuglogPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "block", "init-debug.log")
}

func (o *snapshotter) overlaybdWritableIndexPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "block", "writable_index")
}

func (o *snapshotter) overlaybdWritableDataPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "block", "writable_data")
}

func (o *snapshotter) overlaybdOCILayerPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "layer.tar")
}

func (o *snapshotter) overlaybdOCILayerMeta(id string) string {
	return filepath.Join(o.root, "snapshots", id, "layer.metadata")
}

func (o *snapshotter) overlaybdBackstoreMarkFile(id string) string {
	return filepath.Join(o.root, "snapshots", id, "block", "backstore_mark")
}

func (o *snapshotter) turboOCIGzipIdx(id string) string {
	return filepath.Join(o.root, "snapshots", id, "fs", "gzip.meta")
}

// Close closes the snapshotter
func (o *snapshotter) Close() error {
	return o.ms.Close()
}

func (o *snapshotter) identifyLocalStorageType(filePath string) (storageType, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return storageTypeUnknown, err
	}

	const overlaybdHeaderSize = 32
	data := make([]byte, overlaybdHeaderSize)

	_, err = f.Read(data)
	f.Close()
	if err != nil {
		return storageTypeUnknown, fmt.Errorf("failed to read %s: %w", filePath, err)
	}

	if isOverlaybdFileHeader(data) {
		return storageTypeLocalBlock, nil
	}
	return storageTypeNormal, nil
}

func isOverlaybdFileHeader(header []byte) bool {
	if len(header) < 24 {
		return false
	}
	magic0 := binary.LittleEndian.Uint64(header[0:8])
	magic1 := binary.LittleEndian.Uint64(header[8:16])
	magic2 := binary.LittleEndian.Uint64(header[16:24])
	return (magic0 == 281910587246170 && magic1 == 7384066304294679924 && magic2 == 7017278244700045632) || // "ZFile\x00\x01\x00", "tuji.yyf", "@Alibaba"
		(magic0 == 564050879402828 && magic1 == 5478704352671792741 && magic2 == 9993152565363659426) // "LSMT\x00\x01\x02\x00", …, …
}
