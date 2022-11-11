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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/storage"
	"github.com/containerd/continuity"
	"github.com/moby/sys/mountinfo"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
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

func (o *snapshotter) checkOverlaybdInUse(ctx context.Context, dir string) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer f.Close()
	b, err := o.parseAndCheckMounted(ctx, f, dir)
	if err != nil {
		log.G(ctx).Errorf("Parsing mounts fields, error: %v", err)
		return false, err
	}
	return b, nil
}

func (o *snapshotter) parseAndCheckMounted(ctx context.Context, r io.Reader, dir string) (bool, error) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		if err := s.Err(); err != nil {
			return false, err
		}
		/*
		   489 29 0:51 / /run/containerd/io.containerd.runtime.v2.task/default/testwp1/rootfs rw,relatime shared:272 - overlay overlay rw,lowerdir=/var/lib/containerd/io.containerd.snapshotter.v1.overlaybd/snapshots/2335/block/mountpoint,upperdir=/var/lib/containerd/io.containerd.snapshotter.v1.overlaybd/snapshots/2344/fs,workdir=/var/lib/containerd/io.containerd.snapshotter.v1.overlaybd/snapshots/2344/work
		   (1)(2) (3) (4) (5)                                                                  (6)            (7)   (8) (9)   (10)         (11)
		   (1) mount ID:  unique identifier of the mount (may be reused after umount)
		   (2) parent ID:  ID of parent (or of self for the top of the mount tree)
		   (3) major:minor:  value of st_dev for files on filesystem
		   (4) root:  root of the mount within the filesystem
		   (5) mount point:  mount point relative to the process's root
		   (6) mount options:  per mount options
		   (7) optional fields:  zero or more fields of the form "tag[:value]"
		   (8) separator:  marks the end of the optional fields
		   (9) filesystem type:  name of filesystem of the form "type[.subtype]"
		   (10) mount source:  filesystem specific information or "none"
		   (11) super options:  per super block options
		*/

		text := s.Text()
		fields := strings.Split(text, " ")
		numFields := len(fields)
		if numFields < 10 {
			// should be at least 10 fields
			logrus.Warnf("Parsing '%s' failed: not enough fields (%d)", text, numFields)
			continue
		}

		// one or more optional fields, when a separator (-)
		i := 6
		for ; i < numFields && fields[i] != "-"; i++ {
			switch i {
			case 6:
				// p.Optional = fields[6]
			default:
				/* NOTE there might be more optional fields before the such as
				   fields[7]...fields[N] (where N < sepIndex), although
				   as of Linux kernel 4.15 the only known ones are
				   mount propagation flags in fields[6]. The correct
				   behavior is to ignore any unknown optional fields.
				*/
				break
			}
		}
		if i == numFields {
			log.G(ctx).Warnf("Parsing '%s' failed: missing separator ('-')", text)
			continue
		}

		// There should be 3 fields after the separator...
		if i+4 > numFields {
			log.G(ctx).Warnf("Parsing '%s' failed: not enough fields after a separator", text)
			continue
		}
		// ... but in Linux <= 3.9 mounting a cifs with spaces in a share name
		// (like "//serv/My Documents") _may_ end up having a space in the last field
		// of mountinfo (like "unc=//serv/My Documents"). Since kernel 3.10-rc1, cifs
		// option unc= is ignored,  so a space should not appear. In here we ignore
		// those "extra" fields caused by extra spaces.
		fstype := fields[i+1]
		vfsOpts := fields[i+3]
		if fstype == "overlay" && strings.Contains(vfsOpts, dir) {
			return true, nil
		}
	}
	return false, nil
}

// unmountAndDetachBlockDevice
func (o *snapshotter) unmountAndDetachBlockDevice(ctx context.Context, snID string, snKey string) (err error) {

	var info snapshots.Info
	if snKey != "" {
		_, info, _, err = storage.GetInfo(ctx, snKey)
		if err != nil {
			return errors.Wrapf(err, "can't get snapshot info.")
		}
	}
	writeType := o.getWritableType(ctx, snID, info)
	overlaybd, err := os.ReadFile(o.overlaybdBackstoreMarkFile(snID))
	if err != nil {
		log.G(ctx).Errorf("read device name failed: %s, err: %v", o.overlaybdBackstoreMarkFile(snID), err)
	}
	if writeType != rwDev {
		mountPoint := o.overlaybdMountpoint(snID)
		log.G(ctx).Debugf("check overlaybd mountpoint is in use: %s", mountPoint)
		busy, err := o.checkOverlaybdInUse(ctx, mountPoint)
		if err != nil {
			return err
		}
		if busy {
			log.G(ctx).Infof("device still in use.")
			return nil
		}
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
	log.G(ctx).Infof("destroy overlaybd device success(sn: %s): %s", snID, overlaybd)
	return nil
}

// attachAndMountBlockDevice
//
// TODO(fuweid): need to track the middle state if the process has been killed.
func (o *snapshotter) attachAndMountBlockDevice(ctx context.Context, snID string, writable string, fsType string, mkfs bool) (retErr error) {

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

	if err = os.WriteFile(path.Join(targetPath, "control"), ([]byte)(fmt.Sprintf("dev_config=overlaybd/%s", o.overlaybdConfPath(snID))), 0666); err != nil {
		return errors.Wrapf(err, "failed to write target dev_config for %s", targetPath)
	}

	err = os.WriteFile(path.Join(targetPath, "control"), ([]byte)(fmt.Sprintf("max_data_area_mb=%d", obdMaxDataAreaMB)), 0666)
	if err != nil {
		return errors.Wrapf(err, "failed to write target max_data_area_mb for %s", targetPath)
	}

	err = os.WriteFile(path.Join(targetPath, "enable"), ([]byte)("1"), 0666)
	if err != nil {
		// read the init-debug.log for readable
		debugLogPath := o.overlaybdInitDebuglogPath(snID)
		if data, derr := os.ReadFile(debugLogPath); derr == nil {
			return errors.Errorf("failed to enable target for %s, %s", targetPath, data)
		}
		return errors.Wrapf(err, "failed to enable target for %s", targetPath)
	}

	// fixed by fuweid
	err = os.WriteFile(
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
	err = os.WriteFile(nexusPath, ([]byte)(loopDevID), 0666)
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
	bytes, err := os.ReadFile(devAddressPath)
	if err != nil {
		return errors.Wrapf(err, "failed to read loopback address for %s", devAddressPath)
	}
	deviceNumber := strings.TrimSuffix(string(bytes), "\n")

	// The device doesn't show up instantly. Need retry here.
	var lastErr error = nil
	for retry := 0; retry < maxAttachAttempts; retry++ {
		devDirs, err := os.ReadDir(o.scsiBlockDevicePath(deviceNumber))
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
					log.G(ctx).Infof("fs type: %s, mount options: %s, rw: %s", fstype, data, writable)
					//if err := unix.Mount(device, mountPoint, "ext4", mflag, "discard"); err != nil {
					if err := unix.Mount(device, mountPoint, fstype, mflag, data); err != nil {
						lastErr = errors.Wrapf(err, "failed to mount %s to %s", device, mountPoint)
						time.Sleep(10 * time.Millisecond)
						break // retry
					}
					// fixed by fuweid
					err = os.WriteFile(
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
			if err := os.WriteFile(devSavedPath, []byte(device), 0644); err != nil {
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
		log.G(ctx).Infof("prepare writable layer. (sn: %s)", id)
		if err := o.prepareWritableOverlaybd(ctx, id); err != nil {
			return err
		}

		configJSON.Upper = OverlayBDBSConfigUpper{
			Index: o.overlaybdWritableIndexPath(id),
			Data:  o.overlaybdWritableDataPath(id),
		}
	}
	configBuffer, _ := json.MarshalIndent(configJSON, "", "  ")
	log.G(ctx).Infoln(string(configBuffer))
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
	data, err := os.ReadFile(confPath)
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
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
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
		err := errors.Wrapf(err, "failed to prepare writable overlaybd: %s", out)
		log.G(ctx).Errorln(err)
		return err
	}
	return nil
}

// commitWritableOverlaybd
func (o *snapshotter) commitWritableOverlaybd(ctx context.Context, snID string) (retErr error) {
	binpath := filepath.Join(o.config.OverlayBDUtilBinDir, "overlaybd-commit")

	out, err := exec.CommandContext(ctx, binpath, "-z",
		o.overlaybdWritableDataPath(snID),
		o.overlaybdWritableIndexPath(snID),
		o.magicFilePath(snID)).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to commit writable overlaybd: %s", out)
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
