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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/snapshots/storage"
	"github.com/containerd/continuity"
	"github.com/moby/sys/mountinfo"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

const (
	maxAttachAttempts = 50

	// hba number used to create tcmu devices in configfs
	// all overlaybd devices are configured in /sys/kernel/config/target/core/user_999999999/
	// devices ares identified by their snID /sys/kernel/config/target/core/user_999999999/dev_$snID
	obdHbaNum = 999999999

	// Naa prefix for loopback devices in configfs
	// for example snID 128, the loopback device config in /sys/kernel/config/target/loopback/naa.1990000000000128
	obdLoopNaaPrefix = 199

	// param used to restrict tcmu data area size
	// it is worked by setting max_data_area_mb for devices in configfs.
	obdMaxDataAreaMB = 4
)

// OverlayBDBSConfig is the config of overlaybd target.
type OverlayBDBSConfig struct {
	RepoBlobURL       string                   `json:"repoBlobUrl"`
	Lowers            []OverlayBDBSConfigLower `json:"lowers"`
	Upper             OverlayBDBSConfigUpper   `json:"upper"`
	ResultFile        string                   `json:"resultFile"`
	AccelerationLayer bool                     `json:"accelerationLayer,omitempty"`
	RecordTracePath   string                   `json:"recordTracePath,omitempty"`
}

// OverlayBDBSConfigLower
type OverlayBDBSConfigLower struct {
	File   string `json:"file,omitempty"`
	Digest string `json:"digest,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Dir    string `json:"dir,omitempty"`
}

type OverlayBDBSConfigUpper struct {
	Index string `json:"index,omitempty"`
	Data  string `json:"data,omitempty"`
}

// unmountAndDetachBlockDevice
func (o *snapshotter) unmountAndDetachBlockDevice(ctx context.Context, snID string, snKey string) error {

	_, info, _, err := storage.GetInfo(ctx, snKey)
	if err != nil {
		return errors.Wrapf(err, "can't get snapshot info.")
	}
	writeType := o.getWritableType(ctx, snID, info)

	if writeType != rwDev {
		mountPoint := o.overlaybdMountpoint(snID)
		log.G(ctx).Infof("umount device, mountpoint: %s", mountPoint)
		if err := mount.UnmountAll(mountPoint, 0); err != nil {
			return errors.Wrapf(err, "failed to umount %s", mountPoint)
		}
	}

	loopDevID := o.overlaybdLoopbackDeviceID(snID)
	lunPath := o.overlaybdLoopbackDeviceLunPath(loopDevID)
	linkPath := path.Join(lunPath, "dev_"+snID)

	err = os.RemoveAll(linkPath)
	if err != nil {
		return errors.Wrapf(err, "failed to remove loopback link %s", linkPath)
	}

	err = os.RemoveAll(lunPath)
	if err != nil {
		return errors.Wrapf(err, "failed to remove loopback lun %s", lunPath)
	}

	loopDevPath := o.overlaybdLoopbackDevicePath(loopDevID)
	tpgtPath := path.Join(loopDevPath, "tpgt_1")

	err = os.RemoveAll(tpgtPath)
	if err != nil {
		return errors.Wrapf(err, "failed to remove loopback tgpt %s", tpgtPath)
	}

	err = os.RemoveAll(loopDevPath)
	if err != nil {
		return errors.Wrapf(err, "failed to remove loopback dir %s", loopDevPath)
	}

	targetPath := o.overlaybdTargetPath(snID)

	err = os.RemoveAll(targetPath)
	if err != nil {
		return errors.Wrapf(err, "failed to remove target dir %s", targetPath)
	}
	return nil
}

// attachAndMountBlockDevice
//
// TODO(fuweid): need to track the middle state if the process has been killed.
func (o *snapshotter) attachAndMountBlockDevice(ctx context.Context, snID string, writable int, fsType string, mkfs bool) (retErr error) {

	if err := lookup(o.overlaybdMountpoint(snID)); err == nil {
		return nil
	}

	targetPath := o.overlaybdTargetPath(snID)
	err := os.MkdirAll(targetPath, 0700)
	if err != nil {
		return errors.Wrapf(err, "failed to create target dir for %s", targetPath)
	}

	defer func() {
		if retErr != nil {
			rerr := os.RemoveAll(targetPath)
			if rerr != nil {
				log.G(ctx).WithError(rerr).Warnf("failed to clean target dir %s", targetPath)
			}
		}
	}()

	if err = ioutil.WriteFile(path.Join(targetPath, "control"), ([]byte)(fmt.Sprintf("dev_config=overlaybd/%s", o.overlaybdConfPath(snID))), 0666); err != nil {
		return errors.Wrapf(err, "failed to write target dev_config for %s", targetPath)
	}

	err = ioutil.WriteFile(path.Join(targetPath, "control"), ([]byte)(fmt.Sprintf("max_data_area_mb=%d", obdMaxDataAreaMB)), 0666)
	if err != nil {
		return errors.Wrapf(err, "failed to write target max_data_area_mb for %s", targetPath)
	}

	err = ioutil.WriteFile(path.Join(targetPath, "enable"), ([]byte)("1"), 0666)
	if err != nil {
		// read the init-debug.log for readable
		debugLogPath := o.overlaybdInitDebuglogPath(snID)
		if data, derr := ioutil.ReadFile(debugLogPath); derr == nil {
			return errors.Errorf("failed to enable target for %s, %s", targetPath, data)
		}
		return errors.Wrapf(err, "failed to enable target for %s", targetPath)
	}

	// fixed by fuweid
	err = ioutil.WriteFile(
		path.Join(targetPath, "attrib", "cmd_time_out"),
		([]byte)(fmt.Sprintf("%v", math.MaxInt32/1000)), 0666)
	if err != nil {
		return errors.Wrapf(err, "failed to update cmd_time_out")
	}

	loopDevID := o.overlaybdLoopbackDeviceID(snID)
	loopDevPath := o.overlaybdLoopbackDevicePath(loopDevID)

	err = os.MkdirAll(loopDevPath, 0700)
	if err != nil {
		return errors.Wrapf(err, "failed to create loopback dir %s", loopDevPath)
	}

	tpgtPath := path.Join(loopDevPath, "tpgt_1")
	lunPath := o.overlaybdLoopbackDeviceLunPath(loopDevID)
	err = os.MkdirAll(lunPath, 0700)
	if err != nil {
		return errors.Wrapf(err, "failed to create loopback lun dir %s", lunPath)
	}

	defer func() {
		if retErr != nil {
			rerr := os.RemoveAll(lunPath)
			if rerr != nil {
				log.G(ctx).WithError(rerr).Warnf("failed to clean loopback lun %s", lunPath)
			}

			rerr = os.RemoveAll(tpgtPath)
			if rerr != nil {
				log.G(ctx).WithError(rerr).Warnf("failed to clean loopback tpgt %s", tpgtPath)
			}

			rerr = os.RemoveAll(loopDevPath)
			if rerr != nil {
				log.G(ctx).WithError(rerr).Warnf("failed to clean loopback dir %s", loopDevPath)
			}
		}
	}()

	nexusPath := path.Join(tpgtPath, "nexus")
	err = ioutil.WriteFile(nexusPath, ([]byte)(loopDevID), 0666)
	if err != nil {
		return errors.Wrapf(err, "failed to write loopback nexus %s", nexusPath)
	}

	linkPath := path.Join(lunPath, "dev_"+snID)
	err = os.Symlink(targetPath, linkPath)
	if err != nil {
		return errors.Wrapf(err, "failed to create loopback link %s", linkPath)
	}

	defer func() {
		if retErr != nil {
			rerr := os.RemoveAll(linkPath)
			if err != nil {
				log.G(ctx).WithError(rerr).Warnf("failed to clean loopback link %s", linkPath)
			}
		}
	}()

	devAddressPath := path.Join(tpgtPath, "address")
	bytes, err := ioutil.ReadFile(devAddressPath)
	if err != nil {
		return errors.Wrapf(err, "failed to read loopback address for %s", devAddressPath)
	}
	deviceNumber := strings.TrimSuffix(string(bytes), "\n")

	// The device doesn't show up instantly. Need retry here.
	var lastErr error = nil
	for retry := 0; retry < maxAttachAttempts; retry++ {
		devDirs, err := ioutil.ReadDir(o.scsiBlockDevicePath(deviceNumber))
		if err != nil {
			lastErr = err
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if len(devDirs) == 0 {
			lastErr = errors.Errorf("empty device found")
			time.Sleep(10 * time.Millisecond)
			continue
		}

		for _, dev := range devDirs {
			device := fmt.Sprintf("/dev/%s", dev.Name())
			var mountPoint = o.overlaybdMountpoint(snID)

			options := strings.Split(fsType, ";")
			fstype := options[0]
			data := ""
			if len(options) > 1 {
				data = options[1]
			} else {
				switch fstype {
				case "ext4":
					data = "discard"
				case "xfs":
					data = "nouuid,discard"
				default:
				}
			}

			var mflag uintptr = unix.MS_RDONLY
			if writable != roDir {
				mflag = 0
			}

			if mkfs {
				args := []string{"-t", fstype}
				if len(options) > 2 {
					if options[2] != "" {
						mkfsOpts := strings.Split(options[2], " ")
						args = append(args, mkfsOpts...)
					}
				} else {
					switch fstype {
					case "ext4":
						args = append(args, "-O", "^has_journal,sparse_super,flex_bg", "-G", "1", "-E", "discard", "-F")
					case "xfs":
						args = append(args, "-f", "-l", "size=4m", "-m", "crc=0")
					case "f2fs":
						args = append(args, "-S", "-w", "4096")
					case "ntfs":
						args = append(args, "-F", "-f")
					default:
					}
				}
				args = append(args, device)
				log.G(ctx).Infof("fs type: %s, mkfs options: %v", fstype, args)
				out, err := exec.CommandContext(ctx, "mkfs", args...).CombinedOutput()
				if err != nil {
					return errors.Wrapf(err, "failed to mkfs for dev %s: %s", device, out)
				}
			}

			if writable != rwDev {
				if fstype != "ntfs" {
					log.G(ctx).Infof("fs type: %s, mount options: %s, rw: %d", fstype, data, writable)
					//if err := unix.Mount(device, mountPoint, "ext4", mflag, "discard"); err != nil {
					if err := unix.Mount(device, mountPoint, fstype, mflag, data); err != nil {
						lastErr = errors.Wrapf(err, "failed to mount %s to %s", device, mountPoint)
						time.Sleep(10 * time.Millisecond)
						break // retry
					}
					// fixed by fuweid
					err = ioutil.WriteFile(
						path.Join("/sys/block", dev.Name(), "device", "timeout"),
						([]byte)(fmt.Sprintf("%v", math.MaxInt32/1000)), 0666)
					if err != nil {
						return errors.Wrapf(err, "failed to update timeout")
					}
				} else {
					args := []string{"-t", fstype}
					if writable == roDir {
						args = append(args, "-r")
					}
					if data != "" {
						args = append(args, "-o", data)
					}
					args = append(args, device, mountPoint)
					log.G(ctx).Infof("fs type: %s, mount options: %v", fstype, args)
					out, err := exec.CommandContext(ctx, "mount", args...).CombinedOutput()
					if err != nil {
						lastErr = errors.Wrapf(err, "failed to mount for dev %s: %s", device, out)
						time.Sleep(10 * time.Millisecond)
						break
					}
				}
			}

			devSavedPath := o.overlaybdBackstoreMarkFile(snID)
			if err := ioutil.WriteFile(devSavedPath, []byte(device), 0644); err != nil {
				return errors.Wrapf(err, "failed to create backstore mark file of snapshot %s", snID)
			}
			log.G(ctx).Debugf("write device name: %s into file: %s", device, devSavedPath)
			return nil
		}
	}
	return lastErr
}

// constructOverlayBDSpec generates the config spec for overlaybd target.
func (o *snapshotter) constructOverlayBDSpec(ctx context.Context, key string, writable bool) error {
	id, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return errors.Wrapf(err, "failed to get info for snapshot %s", key)
	}

	stype, err := o.identifySnapshotStorageType(ctx, id, info)
	if err != nil {
		return errors.Wrapf(err, "failed to identify storage of snapshot %s", key)
	}

	configJSON := OverlayBDBSConfig{
		Lowers:     []OverlayBDBSConfigLower{},
		ResultFile: o.overlaybdInitDebuglogPath(id),
	}

	// load the parent's config and reuse the lowerdir
	if info.Parent != "" {
		parentID, _, _, err := storage.GetInfo(ctx, info.Parent)
		if err != nil {
			return errors.Wrapf(err, "failed to get info for parent snapshot %s", info.Parent)
		}

		parentConfJSON, err := o.loadBackingStoreConfig(parentID)
		if err != nil {
			return err
		}
		configJSON.RepoBlobURL = parentConfJSON.RepoBlobURL
		configJSON.Lowers = parentConfJSON.Lowers
	}

	switch stype {
	case storageTypeRemoteBlock:
		if writable {
			return errors.Errorf("remote block device is readonly, not support writable")
		}

		blobSize, err := strconv.Atoi(info.Labels[labelKeyOverlayBDBlobSize])
		if err != nil {
			return errors.Wrapf(err, "failed to parse value of label %s of snapshot %s", labelKeyOverlayBDBlobSize, key)
		}

		blobDigest := info.Labels[labelKeyOverlayBDBlobDigest]
		ref, hasRef := info.Labels[labelKeyImageRef]
		if !hasRef {
			criRef, hasCriRef := info.Labels[labelKeyCriImageRef]
			if !hasCriRef {
				return errors.Errorf("no image-ref label")
			}
			ref = criRef
		}

		blobPrefixURL, err := o.constructImageBlobURL(ref)
		if err != nil {
			return errors.Wrapf(err, "failed to construct image blob prefix url for snapshot %s", key)
		}

		configJSON.RepoBlobURL = blobPrefixURL
		configJSON.Lowers = append(configJSON.Lowers, OverlayBDBSConfigLower{
			Digest: blobDigest,
			Size:   int64(blobSize),
			Dir:    o.upperPath(id),
		})

	case storageTypeLocalBlock:
		if writable {
			return errors.Errorf("local block device is readonly, not support writable")
		}

		configJSON.Lowers = append(configJSON.Lowers, OverlayBDBSConfigLower{
			Dir: o.upperPath(id),
		})

	default:
		if !writable {
			return errors.Errorf("unexpect storage %v of snapshot %v during construct overlaybd spec(writable=%v, parent=%s)", stype, key, writable, info.Parent)
		}

		if err := o.prepareWritableOverlaybd(ctx, id); err != nil {
			return err
		}

		configJSON.Upper = OverlayBDBSConfigUpper{
			Index: o.overlaybdWritableIndexPath(id),
			Data:  o.overlaybdWritableDataPath(id),
		}
	}
	return o.atomicWriteOverlaybdTargetConfig(id, &configJSON)
}

func (o *snapshotter) constructSpecForAccelLayer(id, parentID string) error {
	config, err := o.loadBackingStoreConfig(parentID)
	if err != nil {
		return err
	}
	accelLayerLower := OverlayBDBSConfigLower{Dir: o.upperPath(id)}
	config.Lowers = append(config.Lowers, accelLayerLower)
	return o.atomicWriteOverlaybdTargetConfig(id, config)
}

func (o *snapshotter) updateSpec(snID string, isAccelLayer bool, recordTracePath string) error {
	bsConfig, err := o.loadBackingStoreConfig(snID)
	if err != nil {
		return err
	}
	if isAccelLayer == bsConfig.AccelerationLayer &&
		(recordTracePath == bsConfig.RecordTracePath && recordTracePath == "") {
		// No need to update
		return nil
	}
	bsConfig.RecordTracePath = recordTracePath
	bsConfig.AccelerationLayer = isAccelLayer
	if err = o.atomicWriteOverlaybdTargetConfig(snID, bsConfig); err != nil {
		return err
	}
	return nil
}

// loadBackingStoreConfig loads overlaybd target config.
func (o *snapshotter) loadBackingStoreConfig(snID string) (*OverlayBDBSConfig, error) {
	confPath := o.overlaybdConfPath(snID)
	data, err := ioutil.ReadFile(confPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read config(path=%s) of snapshot %s", confPath, snID)
	}

	var configJSON OverlayBDBSConfig
	if err := json.Unmarshal(data, &configJSON); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal data(%s)", string(data))
	}

	return &configJSON, nil
}

// constructImageBlobURL returns the https://host/v2/<name>/blobs/.
//
// TODO(fuweid): How to know the existing url schema?
func (o *snapshotter) constructImageBlobURL(ref string) (string, error) {
	refspec, err := reference.Parse(ref)
	if err != nil {
		return "", errors.Wrapf(err, "invalid repo url %s", ref)
	}

	host := refspec.Hostname()
	repo := strings.TrimPrefix(refspec.Locator, host+"/")
	return "https://" + path.Join(host, "v2", repo) + "/blobs", nil
}

// atomicWriteOverlaybdTargetConfig
func (o *snapshotter) atomicWriteOverlaybdTargetConfig(snID string, configJSON *OverlayBDBSConfig) error {
	data, err := json.Marshal(configJSON)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal %+v configJSON into JSON", configJSON)
	}

	confPath := o.overlaybdConfPath(snID)
	if err := continuity.AtomicWriteFile(confPath, data, 0600); err != nil {
		return errors.Wrapf(err, "failed to commit the overlaybd config on %s", confPath)
	}
	return nil
}

// prepareWritableOverlaybd
func (o *snapshotter) prepareWritableOverlaybd(ctx context.Context, snID string) error {
	binpath := filepath.Join(o.config.OverlayBDUtilBinDir, "overlaybd-create")

	// TODO(fuweid): 256GB can be configurable?
	out, err := exec.CommandContext(ctx, binpath,
		o.overlaybdWritableDataPath(snID),
		o.overlaybdWritableIndexPath(snID), "64").CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to prepare writable overlaybd: %s", out)
	}
	return nil
}

// commitWritableOverlaybd
func (o *snapshotter) commitWritableOverlaybd(ctx context.Context, snID string) (retErr error) {
	binpath := filepath.Join(o.config.OverlayBDUtilBinDir, "overlaybd-commit")
	tmpPath := filepath.Join(o.root, "snapshots", snID, "block", ".commit-before-zfile")

	out, err := exec.CommandContext(ctx, binpath,
		o.overlaybdWritableDataPath(snID),
		o.overlaybdWritableIndexPath(snID), tmpPath).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to commit writable overlaybd: %s", out)
	}

	defer func() {
		os.Remove(tmpPath)
	}()

	binpath = filepath.Join(o.config.OverlayBDUtilBinDir, "overlaybd-zfile")
	out, err = exec.CommandContext(ctx, binpath, tmpPath, o.magicFilePath(snID)).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to create zfile: %s", out)
	}
	return nil
}

func (o *snapshotter) overlaybdTargetPath(id string) string {
	return fmt.Sprintf("/sys/kernel/config/target/core/user_%d/dev_%s", obdHbaNum, id)
}

func (o *snapshotter) overlaybdLoopbackDeviceID(id string) string {
	paddings := strings.Repeat("0", 13-len(id))
	return fmt.Sprintf("naa.%d%s%s", obdLoopNaaPrefix, paddings, id)
}

func (o *snapshotter) overlaybdLoopbackDevicePath(id string) string {
	return fmt.Sprintf("/sys/kernel/config/target/loopback/%s", id)
}

func (o *snapshotter) overlaybdLoopbackDeviceLunPath(id string) string {
	return fmt.Sprintf("/sys/kernel/config/target/loopback/%s/tpgt_1/lun/lun_0", id)
}

func (o *snapshotter) scsiBlockDevicePath(deviceNumber string) string {
	return fmt.Sprintf("/sys/class/scsi_device/%s:0/device/block", deviceNumber)
}

// TODO: use device number to check?
func lookup(dir string) error {
	dir = filepath.Clean(dir)

	m, err := mountinfo.GetMounts(mountinfo.SingleEntryFilter(dir))
	if err != nil {
		return errors.Wrapf(err, "failed to find the mount info for %q", dir)
	}

	if len(m) == 0 {
		return errors.Errorf("failed to find the mount info for %q", dir)
	}
	return nil
}
