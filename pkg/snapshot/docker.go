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
	"strings"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/storage"
	"github.com/containerd/log"
)

// Docker runtime constants
const (
	DockerInitSuffix = "-init"
)

// IsDockerInitLayer checks if this is a Docker init layer
// Docker uses "-init-key" for prepare and "-init" for committed snapshot
func IsDockerInitLayer(key string) bool {
	return strings.HasSuffix(key, DockerInitSuffix) || strings.Contains(key, "-init-")
}

// IsDockerContainerLayer checks if the parent is a Docker init layer
func IsDockerContainerLayer(parent string) bool {
	if parent == "" {
		return false
	}
	return strings.HasSuffix(parent, DockerInitSuffix) // || strings.Contains(parent, "-init")
}

func (o *snapshotter) dockerWritableModeOrDefault() string {
	if o.dockerWritableMode == "" {
		return DockerWritableOverlayFS
	}
	return o.dockerWritableMode
}

// isDockerOverlayfsMode is the existing lazy-lower + kernel overlayfs upper path.
func (o *snapshotter) isDockerOverlayfsMode() bool {
	return o.runtimeType == "docker" &&
		o.dockerWritableModeOrDefault() == DockerWritableOverlayFS &&
		o.rwMode == RoDir
}

// isDockerNativeMode is the snapshot-capable native OverlayBD writable upper path.
func (o *snapshotter) isDockerNativeMode() bool {
	return o.runtimeType == "docker" && o.dockerWritableModeOrDefault() == DockerWritableNative
}

// isDockerInitLayer checks if this is a Docker init layer for either Docker mode.
func (o *snapshotter) isDockerInitLayer(key string) bool {
	if !o.isDockerOverlayfsMode() && !o.isDockerNativeMode() {
		return false
	}
	return IsDockerInitLayer(key)
}

// isDockerContainerLayer checks if the parent is a Docker init layer.
func (o *snapshotter) isDockerContainerLayer(parent string) bool {
	if !o.isDockerOverlayfsMode() && !o.isDockerNativeMode() {
		return false
	}
	return IsDockerContainerLayer(parent)
}

// prepareDockerInitLayer handles Docker init layer preparation.
// For accelerated images: attach overlaybd device at parent's mountpoint
// For all images: return bind mount to init layer's fs directory
func (o *snapshotter) prepareDockerInitLayer(ctx context.Context, s storage.Snapshot,
	parentID string, parentInfo snapshots.Info, stype storageType) ([]mount.Mount, error) {

	log.G(ctx).Infof("Preparing Docker init layer (sn: %s, parent: %s, stype: %d)", s.ID, parentID, stype)

	// For accelerated images, attach overlaybd device
	if stype == storageTypeLocalBlock || stype == storageTypeRemoteBlock {
		fsType, ok := parentInfo.Labels[label.OverlayBDBlobFsType]
		if !ok {
			if isTurboOCI, _, _ := o.checkTurboOCI(parentInfo.Labels); isTurboOCI {
				_, fsType = o.turboOCIFsMeta(parentID)
			} else {
				fsType = o.defaultFsType
			}
		}

		// Attach and mount overlaybd device at parent's mountpoint (readonly)
		if err := o.attachAndMountBlockDevice(ctx, parentID, RoDir, fsType, false); err != nil {
			return nil, fmt.Errorf("failed to attach overlaybd for docker init layer: %w", err)
		}
	}

	// Return bind mount to init layer's fs directory
	return []mount.Mount{{
		Source:  o.upperPath(s.ID),
		Type:    "bind",
		Options: []string{"rw", "rbind"},
	}}, nil
}

// prepareDockerContainerLayer handles Docker container layer preparation.
// Returns overlay mount with:
//   - upperdir: container layer's fs
//   - lowerdir: init layer's fs + (overlaybd mountpoint OR normal image layers)
//   - workdir: container layer's work
func (o *snapshotter) prepareDockerContainerLayer(ctx context.Context, s storage.Snapshot,
	initID string, initInfo snapshots.Info) ([]mount.Mount, error) {

	log.G(ctx).Infof("Preparing Docker container layer (sn: %s, init: %s, initParent: %s)", s.ID, initID, initInfo.Parent)

	var lowerPaths []string
	// First lowerdir: init layer's fs
	lowerPaths = append(lowerPaths, o.upperPath(initID))

	// Check init layer's parent (image top layer) type
	if initInfo.Parent != "" {
		imageTopID, imageTopInfo, _, err := storage.GetInfo(ctx, initInfo.Parent)
		if err != nil {
			return nil, fmt.Errorf("failed to get image top layer info: %w", err)
		}

		log.G(ctx).Infof("Docker container layer: imageTopID=%s", imageTopID)

		imageStype, err := o.identifySnapshotStorageType(ctx, imageTopID, imageTopInfo)
		if err != nil {
			return nil, err
		}

		log.G(ctx).Infof("Docker container layer: imageStype=%d", imageStype)

		if imageStype == storageTypeLocalBlock || imageStype == storageTypeRemoteBlock {
			// Accelerated image: use overlaybd mountpoint
			mountpoint := o.overlaybdMountpoint(imageTopID)
			log.G(ctx).Infof("Docker container layer: using overlaybd mountpoint=%s", mountpoint)
			lowerPaths = append(lowerPaths, mountpoint)
		} else {
			// Normal image: use parent layers' fs directories from container snapshot
			// s.ParentIDs[0] is init layer (already added above), [1:] are image layers
			if len(s.ParentIDs) > 1 {
				for _, pid := range s.ParentIDs[1:] {
					lowerPaths = append(lowerPaths, o.upperPath(pid))
				}
			}
		}
	} else {
		log.G(ctx).Warnf("Docker container layer: initInfo.Parent is empty!")
	}

	// Build overlay mount options
	var options []string
	if o.indexOff {
		options = append(options, "index=off")
	}
	options = append(options,
		fmt.Sprintf("workdir=%s", o.workPath(s.ID)),
		fmt.Sprintf("upperdir=%s", o.upperPath(s.ID)),
		fmt.Sprintf("lowerdir=%s", strings.Join(lowerPaths, ":")),
	)

	log.G(ctx).Infof("Docker container layer mount options: %v", options)

	return []mount.Mount{{
		Type:    "overlay",
		Source:  "overlay",
		Options: options,
	}}, nil
}

// PrepareDockerLayer handles Docker runtime layer preparation.
// This function is called when preparing init layer or container layer in Docker mode.
func (o *snapshotter) PrepareDockerLayer(ctx context.Context, key string, parent string, s storage.Snapshot,
	parentID string, parentInfo snapshots.Info, kind snapshots.Kind) ([]mount.Mount, storageType, error) {

	if o.isDockerNativeMode() {
		return o.prepareDockerNativeLayer(ctx, key, parent, s, parentID, parentInfo, kind)
	}

	if o.isDockerInitLayer(key) {
		// Docker init layer (overlayfs writable upper)
		var parentStype storageType
		if parent != "" {
			var err error
			parentStype, err = o.identifySnapshotStorageType(ctx, parentID, parentInfo)
			if err != nil {
				return nil, storageTypeUnknown, err
			}
		}
		m, err := o.prepareDockerInitLayer(ctx, s, parentID, parentInfo, parentStype)
		if err != nil {
			return nil, storageTypeUnknown, err
		}
		return m, parentStype, nil

	} else if o.isDockerContainerLayer(parent) {
		// Docker container layer (parent is init layer)
		m, err := o.prepareDockerContainerLayer(ctx, s, parentID, parentInfo)
		if err != nil {
			return nil, storageTypeUnknown, err
		}
		return m, storageTypeNormal, nil
	}

	return nil, storageTypeUnknown, fmt.Errorf("not a Docker layer")
}

func (o *snapshotter) prepareDockerNativeLayer(ctx context.Context, key string, parent string,
	s storage.Snapshot, parentID string, parentInfo snapshots.Info, kind snapshots.Kind) ([]mount.Mount, storageType, error) {

	if o.isDockerInitLayer(key) {
		m, stype, err := o.prepareDockerNativeInitLayer(ctx, key, s, parentID, parentInfo)
		if err != nil {
			return nil, storageTypeUnknown, err
		}
		return m, stype, nil
	}
	if o.isDockerContainerLayer(parent) {
		// View(init) must expose the immutable image base so Moby diff/commit
		// does not compare two aliases of the same mutable mount.
		if kind == snapshots.KindView && isLiveSnapshotLabeled(parentInfo) {
			m, err := o.viewDockerNativeInitBase(ctx, parentID, parentInfo)
			if err != nil {
				return nil, storageTypeUnknown, err
			}
			return m, storageTypeNormal, nil
		}
		m, err := o.prepareDockerNativeContainerLayer(ctx, key, s, parentID, parentInfo)
		if err != nil {
			return nil, storageTypeUnknown, err
		}
		return m, storageTypeNormal, nil
	}
	return nil, storageTypeUnknown, fmt.Errorf("not a Docker native layer")
}

// prepareDockerNativeInitLayer creates one owner OverlayBD device/mount over the
// immutable image parent. Application and Docker init writes share this upper.
func (o *snapshotter) prepareDockerNativeInitLayer(ctx context.Context, key string, s storage.Snapshot,
	parentID string, parentInfo snapshots.Info) ([]mount.Mount, storageType, error) {

	log.G(ctx).Infof("Preparing Docker native init owner (sn: %s, parent: %s)", s.ID, parentID)

	parentStype := storageTypeNormal
	if parentID != "" {
		var err error
		parentStype, err = o.identifySnapshotStorageType(ctx, parentID, parentInfo)
		if err != nil {
			return nil, storageTypeUnknown, err
		}
	}
	if parentStype != storageTypeLocalBlock && parentStype != storageTypeRemoteBlock {
		// Fall back to existing overlayfs Docker init handling for non-accelerated images.
		m, err := o.prepareDockerInitLayer(ctx, s, parentID, parentInfo, parentStype)
		return m, parentStype, err
	}

	// ConstructOverlayBDSpec expects the storage key, not the numeric snapshot ID.
	if err := o.ConstructOverlayBDSpec(ctx, key, true); err != nil {
		return nil, storageTypeUnknown, fmt.Errorf("construct native writable overlaybd spec: %w", err)
	}

	meta, err := o.ensureLiveSnapshotMetadata(s.ID)
	if err != nil {
		return nil, storageTypeUnknown, err
	}

	fsType, ok := parentInfo.Labels[label.OverlayBDBlobFsType]
	if !ok {
		if isTurboOCI, _, _ := o.checkTurboOCI(parentInfo.Labels); isTurboOCI {
			_, fsType = o.turboOCIFsMeta(parentID)
		} else {
			fsType = o.defaultFsType
		}
	}

	if err := o.attachAndMountBlockDeviceWithDevID(ctx, s.ID, RwDir, fsType, true, meta.DeviceID); err != nil {
		return nil, storageTypeUnknown, fmt.Errorf("attach native writable overlaybd: %w", err)
	}

	if saved, err := os.ReadFile(o.overlaybdBackstoreMarkFile(s.ID)); err == nil {
		meta.BlockDevicePath = string(saved)
		if err := o.storeLiveSnapshotMetadata(meta); err != nil {
			return nil, storageTypeUnknown, err
		}
	}

	_, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return nil, storageTypeUnknown, err
	}
	if info.Labels == nil {
		info.Labels = map[string]string{}
	}
	info.Labels[label.OverlayBDDeviceID] = meta.DeviceID
	info.Labels[label.OverlayBDConfigPath] = meta.ConfigPath
	info.Labels[label.OverlayBDDeviceOwner] = s.ID
	info.Labels[label.OverlayBDNativeBaseSnapshot] = parentID
	info.Labels[label.OverlayBDLiveSnapshot] = "true"
	info.Labels[label.SupportReadWriteMode] = "dir"
	if _, err := storage.UpdateInfo(ctx, info); err != nil {
		return nil, storageTypeUnknown, fmt.Errorf("persist native live-snapshot labels: %w", err)
	}

	return []mount.Mount{{
		Source:  meta.Mountpoint,
		Type:    "bind",
		Options: []string{"rw", "rbind"},
	}}, storageTypeLocalBlock, nil
}

// prepareDockerNativeContainerLayer aliases the init owner's mount/device so
// there is exactly one writable OverlayBD upper for init+container writes.
func (o *snapshotter) prepareDockerNativeContainerLayer(ctx context.Context, key string, s storage.Snapshot,
	initID string, initInfo snapshots.Info) ([]mount.Mount, error) {

	log.G(ctx).Infof("Preparing Docker native container alias (sn: %s, owner: %s)", s.ID, initID)

	if initInfo.Labels[label.OverlayBDLiveSnapshot] != "true" &&
		initInfo.Labels[label.OverlayBDDeviceID] == "" {
		// Owner was prepared under overlayfs mode or is not live-snapshot capable.
		return o.prepareDockerContainerLayer(ctx, s, initID, initInfo)
	}

	if err := ensureSingleLiveSnapshotChild(ctx, initInfo.Name, key); err != nil {
		return nil, err
	}

	meta, err := o.loadLiveSnapshotMetadata(initID)
	if err != nil {
		return nil, fmt.Errorf("load owner live-snapshot metadata: %w", err)
	}

	if err := o.ensureLiveSnapshotOwnerMounted(ctx, initID, meta); err != nil {
		return nil, err
	}

	_, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return nil, err
	}
	if info.Labels == nil {
		info.Labels = map[string]string{}
	}
	info.Labels[label.OverlayBDDeviceID] = meta.DeviceID
	info.Labels[label.OverlayBDConfigPath] = meta.ConfigPath
	info.Labels[label.OverlayBDDeviceOwner] = initID
	info.Labels[label.OverlayBDLiveSnapshot] = "true"
	if base := initInfo.Labels[label.OverlayBDNativeBaseSnapshot]; base != "" {
		info.Labels[label.OverlayBDNativeBaseSnapshot] = base
	}
	if _, err := storage.UpdateInfo(ctx, info); err != nil {
		return nil, fmt.Errorf("persist native container alias labels: %w", err)
	}

	return []mount.Mount{{
		Source:  meta.Mountpoint,
		Type:    "bind",
		Options: []string{"rw", "rbind"},
	}}, nil
}

// viewDockerNativeInitBase returns a readonly bind of the immutable image base
// under a live-snapshot init owner. Moby uses View(init) for diff/commit.
func (o *snapshotter) viewDockerNativeInitBase(ctx context.Context, initID string, initInfo snapshots.Info) ([]mount.Mount, error) {
	baseID := initInfo.Labels[label.OverlayBDNativeBaseSnapshot]
	if baseID == "" {
		return nil, fmt.Errorf("live-snapshot init %s missing native base snapshot", initID)
	}

	fsType := o.defaultFsType
	if initInfo.Parent != "" {
		_, parentInfo, _, err := storage.GetInfo(ctx, initInfo.Parent)
		if err == nil {
			if ft, ok := parentInfo.Labels[label.OverlayBDBlobFsType]; ok {
				fsType = ft
			} else if isTurboOCI, _, _ := o.checkTurboOCI(parentInfo.Labels); isTurboOCI {
				_, fsType = o.turboOCIFsMeta(baseID)
			}
		}
	}

	if err := o.attachAndMountBlockDevice(ctx, baseID, RoDir, fsType, false); err != nil {
		return nil, fmt.Errorf("attach native base snapshot %s for View(init): %w", baseID, err)
	}

	return []mount.Mount{{
		Source:  o.overlaybdMountpoint(baseID),
		Type:    "bind",
		Options: []string{"ro", "rbind"},
	}}, nil
}

func (o *snapshotter) ensureLiveSnapshotOwnerMounted(ctx context.Context, ownerID string, meta *liveSnapshotMetadata) error {
	if meta == nil {
		var err error
		meta, err = o.loadLiveSnapshotMetadata(ownerID)
		if err != nil {
			return fmt.Errorf("load owner live-snapshot metadata: %w", err)
		}
	}
	fsType := o.defaultFsType
	if err := o.attachAndMountBlockDeviceWithDevID(ctx, ownerID, RwDir, fsType, false, meta.DeviceID); err != nil {
		return fmt.Errorf("ensure owner overlaybd device mounted: %w", err)
	}
	return nil
}

// mountsDockerNativeLiveSnapshot returns a bind mount to the shared owner
// mountpoint for native live-snapshot owners and container aliases.
func (o *snapshotter) mountsDockerNativeLiveSnapshot(ctx context.Context, id string, info snapshots.Info) ([]mount.Mount, error) {
	ownerID := info.Labels[label.OverlayBDDeviceOwner]
	if ownerID == "" {
		return nil, fmt.Errorf("live-snapshot snapshot %s missing device owner", id)
	}
	meta, err := o.loadLiveSnapshotMetadata(ownerID)
	if err != nil {
		return nil, fmt.Errorf("load owner live-snapshot metadata: %w", err)
	}
	if err := o.ensureLiveSnapshotOwnerMounted(ctx, ownerID, meta); err != nil {
		return nil, err
	}
	return []mount.Mount{{
		Source:  meta.Mountpoint,
		Type:    "bind",
		Options: []string{"rw", "rbind"},
	}}, nil
}

// RemoveDockerLayer handles Docker init layer removal.
// It checks and destroys the parent overlaybd device if applicable.
func (o *snapshotter) RemoveDockerLayer(ctx context.Context, key string, initID string, info snapshots.Info) error {
	if !o.isDockerInitLayer(key) {
		return nil
	}

	log.G(ctx).Infof("RemoveDockerLayer: handling init layer removal (key: %s, id: %s)", key, initID)

	// Get parent key from info.Parent (this is the image top layer key)
	parentKey := info.Parent
	if parentKey == "" {
		log.G(ctx).Warnf("RemoveDockerLayer: init layer has no parent")
		return nil
	}

	// Get the parent (image top layer) info
	parentID, parentInfo, _, err := storage.GetInfo(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to get parent info for %s: %w", parentKey, err)
	}

	o.locker.Lock(parentID)
	defer o.locker.Unlock(parentID)

	parentStype, err := o.identifySnapshotStorageType(ctx, parentID, parentInfo)
	if err != nil {
		return fmt.Errorf("failed to identify parent storage type: %w", err)
	}

	log.G(ctx).Infof("RemoveDockerLayer: parent (id: %s, stype: %d)", parentID, parentStype)

	if parentStype == storageTypeLocalBlock || parentStype == storageTypeRemoteBlock {
		if _, err := os.Stat(o.overlaybdBackstoreMarkFile(parentID)); err == nil {
			log.G(ctx).Infof("RemoveDockerLayer: destroying overlaybd device for parent %s", parentID)
			if err = o.UnmountAndDetachBlockDevice(ctx, parentID, parentInfo.Name, key); err != nil {
				log.G(ctx).Warnf("failed to destroy overlaybd device for parent %s: %v", parentID, err)
				// Don't block remove, device might still be in use by other containers
			}
		} else {
			log.G(ctx).Debugf("RemoveDockerLayer: no backstore mark file for parent %s", parentID)
		}
	}

	return nil
}

// dockerContainerLayerMount handles Docker container layer mounting logic
func (o *snapshotter) dockerContainerLayerMount(ctx context.Context,
	parentInfo snapshots.Info, s storage.Snapshot, parentID string) ([]mount.Mount, error) {

	// Get init layer's parent (the image top layer key)
	initParentKey := parentInfo.Parent
	if initParentKey == "" {
		log.G(ctx).Warnf("Mounts: Docker container layer has no init parent, falling back to normal mount")
		return o.normalOverlayMount(s), nil
	}

	// Get image top layer info
	imageTopID, imageTopInfo, _, err := storage.GetInfo(ctx, initParentKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get info of image top layer %s: %w", initParentKey, err)
	}

	// Identify the storage type of image top layer
	imageStype, err := o.identifySnapshotStorageType(ctx, imageTopID, imageTopInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to identify storage type of image top layer %s: %w", initParentKey, err)
	}

	if imageStype == storageTypeRemoteBlock || imageStype == storageTypeLocalBlock {
		log.G(ctx).Infof("Mounts: Docker container layer using overlaybd mountpoint=%s", o.overlaybdMountpoint(imageTopID))

		// Build mount options: lowerdir = init/fs : overlaybd_mountpoint
		var options []string
		if o.indexOff {
			options = append(options, "index=off")
		}
		options = append(options,
			fmt.Sprintf("workdir=%s", o.workPath(s.ID)),
			fmt.Sprintf("upperdir=%s", o.upperPath(s.ID)),
			fmt.Sprintf("lowerdir=%s:%s", o.upperPath(parentID), o.overlaybdMountpoint(imageTopID)),
		)

		log.G(ctx).Infof("Mounts: Docker container layer mount options: %v", options)
		return []mount.Mount{
			{
				Type:    "overlay",
				Source:  "overlay",
				Options: options,
			},
		}, nil
	}

	// Normal image: fall back to standard overlay mount
	// s.ParentIDs already contains [initLayerID, imageLayer1ID, ...]
	return o.normalOverlayMount(s), nil
}
