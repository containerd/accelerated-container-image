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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/storage"
	"github.com/containerd/continuity/fs"
	"github.com/moby/locker"
	"github.com/pkg/errors"
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
)

// support on-demand loading by the labels
const (
	// labelKeyTargetSnapshotRef is the interface to know that Prepare
	// action is to pull image, not for container Writable snapshot.
	//
	// NOTE: Only available in >= containerd 1.4.0 and containerd.Pull
	// with Unpack option.
	//
	// FIXME(fuweid): With containerd design, we don't know that what purpose
	// snapshotter.Prepare does for. For unpacked image, prepare is for
	// container's rootfs. For pulling image, the prepare is for committed.
	// With label "containerd.io/snapshot.ref" in preparing, snapshotter
	// author will know it is for pulling image. It will be useful.
	//
	// The label is only propagated during pulling image. So, is it possible
	// to propagate by image.Unpack()?
	labelKeyTargetSnapshotRef = "containerd.io/snapshot.ref"

	// labelKeyImageRef is the label to mark where the snapshot comes from.
	//
	// TODO(fuweid): Is it possible to use it in upstream?
	labelKeyImageRef = "containerd.io/snapshot/image-ref"

	// labelKeyOverlayBDBlobDigest is the annotation key in the manifest to
	// describe the digest of blob in OverlayBD format.
	//
	// NOTE: The annotation is part of image layer blob's descriptor.
	labelKeyOverlayBDBlobDigest = "containerd.io/snapshot/overlaybd/blob-digest"

	// labelKeyOverlayBDBlobSize is the annotation key in the manifest to
	// describe the size of blob in OverlayBD format.
	//
	// NOTE: The annotation is part of image layer blob's descriptor.
	labelKeyOverlayBDBlobSize = "containerd.io/snapshot/overlaybd/blob-size"

	// labelKeyOverlayBDBlobFsType is the annotation key in the manifest to
	// describe the filesystem type to be mounted as of blob in OverlayBD format.
	//
	// NOTE: The annotation is part of image layer blob's descriptor.
	labelKeyOverlayBDBlobFsType = "containerd.io/snapshot/overlaybd/blob-fs-type"

	// labelKeyAccelerationLayer is the annotation key in the manifest to indicate
	// whether a top layer is acceleration layer or not.
	labelKeyAccelerationLayer = "containerd.io/snapshot/overlaybd/acceleration-layer"

	// labelKeyRecordTrace tells snapshotter to record trace
	labelKeyRecordTrace = "containerd.io/snapshot/overlaybd/record-trace"

	// labelKeyRecordTracePath is the the file path to record trace
	labelKeyRecordTracePath = "containerd.io/snapshot/overlaybd/record-trace-path"

	// labelKeyCriImageRef is thr image-ref from cri
	labelKeyCriImageRef = "containerd.io/snapshot/cri.image-ref"

	remoteLabel    = "containerd.io/snapshot/remote"
	remoteLabelVal = "remote snapshot"
)

// interface
const (
	// LabelSupportReadWriteMode is used to support writable block device
	// for active snapshotter.
	//
	// By default, multiple active snapshotters can share one block device
	// from parent snapshotter(committed). Like image builder and
	// sandboxed-like container runtime(KataContainer, Firecracker), those
	// cases want to use the block device alone or as writable.
	// There are two ways to provide writable devices:
	//  - 'dir' mark the snapshotter
	//    as wriable block device and mount it on rootfs.
	//  - 'dev' mark the snapshotter
	//    as wriable block device without mount.
	LabelSupportReadWriteMode = "containerd.io/snapshot/overlaybd.writable"

	// LabelLocalOverlayBDPath is used to export the commit file path.
	//
	// NOTE: Only used in image build.
	LabelLocalOverlayBDPath = "containerd.io/snapshot/overlaybd.localcommitpath"
)

const (
	roDir = "overlayfs" // overlayfs as rootfs. upper + lower (overlaybd)
	rwDir = "dir"       // mount overlaybd as rootfs
	rwDev = "dev"       // use overlaybd directly
)

type BootConfig struct {
	Address         string `json:"address"`
	Root            string `json:"root"`
	LogLevel        string `json:"verbose"`
	LogReportCaller bool   `json:"logReportCaller"`
	Mode            string `json:"mode"` // fs, dir or dev
	AutoRemoveDev   bool   `json:"autoRemoveDev"`
}

func DefaultBootConfig() *BootConfig {
	return &BootConfig{
		LogLevel:        "info",
		Mode:            "overlayfs",
		LogReportCaller: false,
		AutoRemoveDev:   false,
	}
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
	root           string
	mode           string
	config         SnapshotterConfig
	metacopyOption string
	ms             *storage.MetaStore
	indexOff       bool
	autoRemoveDev  bool

	locker *locker.Locker
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

	metacopyOption := ""
	if _, err := os.Stat("/sys/module/overlay/parameters/metacopy"); err == nil {
		metacopyOption = "metacopy=on"
	}

	// figure out whether "index=off" option is recognized by the kernel
	var indexOff bool
	if _, err = os.Stat("/sys/module/overlay/parameters/index"); err == nil {
		indexOff = true
	}

	return &snapshotter{
		root:           bootConfig.Root,
		mode:           bootConfig.Mode,
		ms:             ms,
		indexOff:       indexOff,
		config:         config,
		metacopyOption: metacopyOption,
		autoRemoveDev:  bootConfig.AutoRemoveDev,
		locker:         locker.New(),
	}, nil
}

// Stat returns the info for an active or committed snapshot by the key.
func (o *snapshotter) Stat(ctx context.Context, key string) (snapshots.Info, error) {
	log.G(ctx).Debugf("Stat (key: %s)", key)

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
func (o *snapshotter) Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error) {
	log.G(ctx).Debugf("Update (fieldpaths: %s)", fieldpaths)

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
func (o *snapshotter) Usage(ctx context.Context, key string) (snapshots.Usage, error) {
	log.G(ctx).Debugf("Usage (key: %s)", key)

	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return snapshots.Usage{}, err
	}

	id, info, usage, err := storage.GetInfo(ctx, key)
	t.Rollback()
	if err != nil {
		return snapshots.Usage{}, err
	}

	if info.Kind == snapshots.KindActive {
		upperPath := o.upperPath(id)
		du, err := fs.DiskUsage(ctx, upperPath)
		if err != nil {
			return snapshots.Usage{}, err
		}
		usage = snapshots.Usage(du)
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
			return roDir
		}
	} else {
		log.G(ctx).Debugf("empty snID get. It should be an initial layer.")
	}
	// overlaybd
	rwMode := func(m string) string {
		if m == "dir" {
			return rwDir
		}
		if m == "dev" {
			return rwDev
		}
		return roDir
	}
	m, ok := info.Labels[LabelSupportReadWriteMode]
	if !ok {
		return rwMode(o.mode)
	}

	return rwMode(m)
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
			return nil, errors.Wrapf(err, "failed to get info of parent snapshot %s", parent)
		}
	}

	// If the layer is in overlaybd format, construct backstore spec and saved it into snapshot dir.
	// Return ErrAlreadyExists to skip pulling and unpacking layer. See https://github.com/containerd/containerd/blob/master/docs/remote-snapshotter.md#snapshotter-apis-for-querying-remote-snapshots
	// This part code is only for Pull.
	if targetRef, ok := info.Labels[labelKeyTargetSnapshotRef]; ok {

		// Acceleration layer will not use remote snapshot. It still needs containerd to pull,
		// unpack blob and commit snapshot. So return a normal mount point here.
		isAccelLayer := info.Labels[labelKeyAccelerationLayer]
		log.G(ctx).Debugf("Prepare (targetRefLabel: %s, accelLayerLabel: %s)", targetRef, isAccelLayer)
		if isAccelLayer == "yes" {
			if err := o.constructSpecForAccelLayer(id, parentID); err != nil {
				return nil, errors.Wrapf(err, "constructSpecForAccelLayer failed: id %s", id)
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
		if stype == storageTypeRemoteBlock {
			info.Labels[remoteLabel] = remoteLabelVal // Mark this snapshot as remote
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
			return nil, errors.Wrapf(errdefs.ErrAlreadyExists, "target snapshot %q", targetRef)
		}
	}

	stype := storageTypeNormal
	writeType := o.getWritableType(ctx, parentID, info)

	// If Preparing for rootfs, find metadata from its parent (top layer), launch and mount backstore device.
	if _, ok := info.Labels[labelKeyTargetSnapshotRef]; !ok {
		log.G(ctx).Infof("Preparing rootfs. writeType: %s", writeType)
		if writeType != roDir {
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
				parentIsAccelLayer := parentInfo.Labels[labelKeyAccelerationLayer] == "yes"
				needRecordTrace := info.Labels[labelKeyRecordTrace] == "yes"
				recordTracePath := info.Labels[labelKeyRecordTracePath]
				log.G(ctx).Debugf("Prepare rootfs (parentIsAccelLayer: %t, needRecordTrace: %t, recordTracePath: %s)",
					parentIsAccelLayer, needRecordTrace, recordTracePath)

				if parentIsAccelLayer {
					log.G(ctx).Debugf("get accel-layer in parent (id: %s)", id)
					// If parent is already an acceleration layer, there is certainly no need to record trace.
					// Just mark this layer to get accelerated (trace replay)
					err = o.updateSpec(parentID, true, "")
					if writeType != roDir {
						o.updateSpec(id, true, "")
					}
				} else if needRecordTrace && recordTracePath != "" {
					err = o.updateSpec(parentID, false, recordTracePath)
				} else {
					// For the compatibility of images which have no accel layer
					err = o.updateSpec(parentID, false, "")
				}
				if err != nil {
					return nil, errors.Wrapf(err, "updateSpec failed for snapshot %s", parentID)
				}
			}

			var obdID string
			var obdInfo *snapshots.Info
			if writeType != roDir {
				obdID = id
				obdInfo = &info
			} else {
				obdID = parentID
				obdInfo = &parentInfo
			}
			fsType, ok := obdInfo.Labels[labelKeyOverlayBDBlobFsType]
			if !ok {
				log.G(ctx).Warnf("cannot get fs type from label, %v", obdInfo.Labels)
				fsType = "ext4"
			}
			log.G(ctx).Debugf("attachAndMountBlockDevice (obdID: %s, writeType: %s, fsType %s, targetPath: %s)",
				obdID, writeType, fsType, o.overlaybdTargetPath(obdID))
			if err = o.attachAndMountBlockDevice(ctx, obdID, writeType, fsType, parent == ""); err != nil {
				log.G(ctx).Errorf("%v", err)
				return nil, errors.Wrapf(err, "failed to attach and mount for snapshot %v", obdID)
			}

			defer func() {
				if retErr != nil && writeType == rwDir { // It's unnecessary to umount overlay block device if writeType == writeTypeRawDev
					if rerr := mount.Unmount(o.overlaybdMountpoint(obdID), 0); rerr != nil {
						log.G(ctx).WithError(rerr).Warnf("failed to umount writable block %s", o.overlaybdMountpoint(obdID))
					}
				}
			}()
		default:
			// do nothing
		}
	}

	rollback = false
	if err := t.Commit(); err != nil {
		return nil, err
	}

	var m []mount.Mount
	switch stype {
	case storageTypeNormal:
		m = o.normalOverlayMount(s)
		log.G(ctx).Debugf("return mount point(R/W mode: %s): %v", writeType, m)
	case storageTypeLocalBlock, storageTypeRemoteBlock:
		m, err = o.basedOnBlockDeviceMount(ctx, s, writeType)
		if err != nil {
			return nil, errors.Wrapf(err, "%s", err.Error())
		}
	default:
		panic("unreachable")
	}
	return m, nil

}

// Prepare creates an active snapshot identified by key descending from the provided parent.
func (o *snapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) (_ []mount.Mount, retErr error) {

	return o.createMountPoint(ctx, snapshots.KindActive, key, parent, opts...)
}

// View returns a readonly view on parent snapshotter.
func (o *snapshotter) View(ctx context.Context, key, parent string, opts ...snapshots.Opt) (_ []mount.Mount, retErr error) {
	log.G(ctx).Debugf("View (key: %s, parent: %s)", key, parent)
	defer log.G(ctx).Debugf("return View (key: %s, parent: %s)", key, parent)
	return o.createMountPoint(ctx, snapshots.KindView, key, parent, opts...)
}

// Mounts returns the mounts for the transaction identified by key. Can be
// called on an read-write or readonly transaction.
//
// This can be used to recover mounts after calling View or Prepare.
func (o *snapshotter) Mounts(ctx context.Context, key string) ([]mount.Mount, error) {
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		return nil, err
	}
	defer t.Rollback()

	s, err := storage.GetSnapshot(ctx, key)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get active mount")
	}

	log.G(ctx).Debugf("Mounts (key: %s, id: %s, parentID: %s, kind: %d)", key, s.ID, s.ParentIDs, s.Kind)

	if len(s.ParentIDs) > 0 {
		o.locker.Lock(s.ID)
		defer o.locker.Unlock(s.ID)
		o.locker.Lock(s.ParentIDs[0])
		defer o.locker.Unlock(s.ParentIDs[0])

		_, info, _, err := storage.GetInfo(ctx, key)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get info")
		}

		writeType := o.getWritableType(ctx, s.ID, info)
		if writeType != roDir {
			return o.basedOnBlockDeviceMount(ctx, s, writeType)
		}

		parentID, parentInfo, _, err := storage.GetInfo(ctx, info.Parent)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get info of parent snapshot %s", info.Parent)
		}

		parentStype, err := o.identifySnapshotStorageType(ctx, parentID, parentInfo)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to identify storage of parent snapshot %s", parentInfo.Name)
		}

		if parentStype == storageTypeRemoteBlock || parentStype == storageTypeLocalBlock {
			fsType, ok := parentInfo.Labels[labelKeyOverlayBDBlobFsType]
			if !ok {
				fsType = "ext4"
			}
			if err := o.attachAndMountBlockDevice(ctx, parentID, roDir, fsType, false); err != nil {
				return nil, errors.Wrapf(err, "failed to attach and mount for snapshot %v", key)
			}
			return o.basedOnBlockDeviceMount(ctx, s, roDir)
		}
	}
	return o.normalOverlayMount(s), nil
}

// Commit
func (o *snapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) (retErr error) {
	log.G(ctx).Debugf("Commit (key: %s, name: %s)", key, name)

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
		return errors.Wrapf(err, "failed to get info of snapshot %s", key)
	}

	// if writable, should commit the data and make it immutable.
	if _, writableBD := oinfo.Labels[LabelSupportReadWriteMode]; writableBD {
		// TODO(fuweid): how to rollback?
		if oinfo.Labels[labelKeyAccelerationLayer] == "yes" {
			log.G(ctx).Info("Commit accel-layer requires no writable_data")
		} else {
			if err := o.unmountAndDetachBlockDevice(ctx, id, key); err != nil {
				return errors.Wrapf(err, "failed to destroy target device for snapshot %s", key)
			}

			if err := o.commitWritableOverlaybd(ctx, id); err != nil {
				return err
			}

			defer func() {
				if retErr != nil {
					return
				}

				// clean up the temporary data
				os.Remove(o.overlaybdWritableDataPath(id))
				os.Remove(o.overlaybdWritableIndexPath(id))
			}()

			opts = append(opts, snapshots.WithLabels(map[string]string{LabelLocalOverlayBDPath: o.magicFilePath(id)}))
		}
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

	if stype == storageTypeLocalBlock {
		if err := o.constructOverlayBDSpec(ctx, name, false); err != nil {
			return errors.Wrapf(err, "failed to construct overlaybd config")
		}

		if info.Labels == nil {
			info.Labels = make(map[string]string)
		}

		info.Labels[LabelLocalOverlayBDPath] = o.magicFilePath(id)
		info, err = storage.UpdateInfo(ctx, info, fmt.Sprintf("labels.%s", LabelLocalOverlayBDPath))
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
		return "", snapshots.Info{}, errors.Wrap(err, "failed to commit snapshot")
	}

	id, info, _, err := storage.GetInfo(ctx, name)
	if err != nil {
		return "", snapshots.Info{}, err
	}
	return id, info, nil
}

// Remove abandons the snapshot identified by key. The snapshot will
// immediately become unavailable and unrecoverable.
func (o *snapshotter) Remove(ctx context.Context, key string) (err error) {
	log.G(ctx).Debugf("Remove (key: %s)", key)

	ctx, t, err := o.ms.TransactionContext(ctx, true)
	if err != nil {
		return err
	}

	rollback := true
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
				return errors.Wrapf(err, "failed to destroy target device for snapshot %s", key)
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
					return errors.Wrapf(err, "failed to destroy target device for snapshot %s", key)
				}
			}
		}
	}

	defer func() {
		if err != nil && rollback {
			if rerr := t.Rollback(); rerr != nil {
				log.G(ctx).WithError(rerr).Warn("failed to rollback transaction")
			}
		}
	}()

	_, _, err = storage.Remove(ctx, key)
	if err != nil {
		return errors.Wrap(err, "failed to remove")
	}

	if err := os.RemoveAll(o.snPath(id)); err != nil && !os.IsNotExist(err) {
		return err
	}

	rollback = false
	return t.Commit()
}

// Walk the snapshots.
func (o *snapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, fs ...string) error {
	log.G(ctx).Debugf("Walk (fs: %s)", fs)

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
		return "", errors.Wrap(err, "failed to create temp dir")
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
	if s.Kind == snapshots.KindView && (writeType == rwflag || writeType == rwDev) {
		log.G(ctx).Infof("snapshot.Kind == View, reset overlaybd as READ-ONLY.")
		rwflag = "ro"
	}
	if writeType == rwDir {
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
	if writeType == rwDev {
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
					err = errors.Wrapf(err, "failed to remove path: %v", err1)
				}
			}
		}
	}()

	snapshotDir := filepath.Join(o.root, "snapshots")
	td, err = o.prepareDirectory(ctx, snapshotDir, kind)
	if err != nil {
		return "", snapshots.Info{}, errors.Wrap(err, "failed to create prepare snapshot dir")
	}

	s, err := storage.CreateSnapshot(ctx, kind, key, parent, opts...)
	if err != nil {
		return "", snapshots.Info{}, errors.Wrap(err, "failed to create snapshot")
	}

	if len(s.ParentIDs) > 0 {
		st, err := os.Stat(o.upperPath(s.ParentIDs[0]))
		if err != nil {
			return "", snapshots.Info{}, errors.Wrap(err, "failed to stat parent")
		}

		stat := st.Sys().(*syscall.Stat_t)
		if err := os.Lchown(filepath.Join(td, "fs"), int(stat.Uid), int(stat.Gid)); err != nil {
			return "", snapshots.Info{}, errors.Wrap(err, "failed to chown")
		}
	}

	path = filepath.Join(snapshotDir, s.ID)
	if err = os.Rename(td, path); err != nil {
		return "", snapshots.Info{}, errors.Wrap(err, "failed to rename")
	}
	td = ""

	id, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return "", snapshots.Info{}, errors.Wrap(err, "failed to get snapshot info")
	}
	return id, info, nil
}

func (o *snapshotter) identifySnapshotStorageType(ctx context.Context, id string, info snapshots.Info) (storageType, error) {
	if _, ok := info.Labels[labelKeyTargetSnapshotRef]; ok {
		_, hasBDBlobSize := info.Labels[labelKeyOverlayBDBlobSize]
		_, hasBDBlobDigest := info.Labels[labelKeyOverlayBDBlobDigest]
		_, hasRef := info.Labels[labelKeyImageRef]
		_, hasCriRef := info.Labels[labelKeyCriImageRef]

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

	// check writable data file
	filePath = o.overlaybdWritableDataPath(id)
	st, err = o.identifyLocalStorageType(filePath)
	if err == nil {
		return st, nil
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

func (o *snapshotter) snPath(id string) string {
	return filepath.Join(o.root, "snapshots", id)
}

func (o *snapshotter) upperPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "fs")
}

func (o *snapshotter) workPath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "work")
}

func (o *snapshotter) magicFilePath(id string) string {
	return filepath.Join(o.root, "snapshots", id, "fs", "overlaybd.commit")
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

func (o *snapshotter) overlaybdBackstoreMarkFile(id string) string {
	return filepath.Join(o.root, "snapshots", id, "block", "backstore_mark")
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
		return storageTypeUnknown, errors.Wrapf(err, "failed to read %s", filePath)
	}

	if isOverlaybdFileHeader(data) {
		return storageTypeLocalBlock, nil
	}
	return storageTypeNormal, nil
}

func isOverlaybdFileHeader(header []byte) bool {
	magic0 := *(*uint64)(unsafe.Pointer(&header[0]))
	magic1 := *(*uint64)(unsafe.Pointer(&header[8]))
	magic2 := *(*uint64)(unsafe.Pointer(&header[16]))
	return (magic0 == 281910587246170 && magic1 == 7384066304294679924 && magic2 == 7017278244700045632) ||
		(magic0 == 564050879402828 && magic1 == 5478704352671792741 && magic2 == 9993152565363659426)
}
