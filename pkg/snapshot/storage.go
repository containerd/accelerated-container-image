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
	"encoding/binary"
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

	sn "github.com/containerd/accelerated-container-image/pkg/types"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/accelerated-container-image/pkg/utils"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/storage"
	"github.com/containerd/containerd/v2/pkg/reference"
	"github.com/containerd/continuity"
	"github.com/containerd/continuity/fs"
	"github.com/containerd/log"
	"github.com/moby/sys/mountinfo"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
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

	// Just in case someone really needs to force to ext4
	ext4FSFallbackFile = ".TurboOCI_ext4"
)

type mountMatcherFunc func(fields []string, separatorIndex int) bool

func (o *snapshotter) checkOverlaybdInUse(ctx context.Context, dir string) (bool, error) {
	matcher := func(fields []string, separatorIndex int) bool {
		// ... but in Linux <= 3.9 mounting a cifs with spaces in a share name
		// (like "//serv/My Documents") _may_ end up having a space in the last field
		// of mountinfo (like "unc=//serv/My Documents"). Since kernel 3.10-rc1, cifs
		// option unc= is ignored,  so a space should not appear. In here we ignore
		// those "extra" fields caused by extra spaces.
		fstype := fields[separatorIndex+1]
		vfsOpts := fields[separatorIndex+3]
		return fstype == "overlay" && strings.Contains(vfsOpts, dir)
	}
	return o.matchMounts(ctx, matcher)
}

func (o *snapshotter) isMounted(ctx context.Context, mountpoint string) (bool, error) {
	matcher := func(fields []string, separatorIndex int) bool {
		mp := fields[4]
		return path.Clean(mountpoint) == path.Clean(mp)
	}
	return o.matchMounts(ctx, matcher)
}

func (o *snapshotter) matchMounts(ctx context.Context, matcher mountMatcherFunc) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer f.Close()
	b, err := o.parseAndCheckMounted(ctx, f, matcher)
	if err != nil {
		log.G(ctx).Errorf("Parsing mounts fields, error: %v", err)
		return false, err
	}
	return b, nil
}

func (o *snapshotter) parseAndCheckMounted(ctx context.Context, r io.Reader, matcher mountMatcherFunc) (bool, error) {
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
			log.G(ctx).Warnf("Parsing '%s' failed: not enough fields (%d)", text, numFields)
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

		if matcher(fields, i) {
			return true, nil
		}
	}
	return false, nil
}

// unmountAndDetachBlockDevice will do nothing if the device is already destroyed
func (o *snapshotter) unmountAndDetachBlockDevice(ctx context.Context, snID string, snKey string) (err error) {
	devName, err := os.ReadFile(o.overlaybdBackstoreMarkFile(snID))
	if err != nil {
		log.G(ctx).Errorf("read device name failed: %s, err: %v", o.overlaybdBackstoreMarkFile(snID), err)
	}

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
		return fmt.Errorf("failed to umount %s: %w", mountPoint, err)
	}

	loopDevID := o.overlaybdLoopbackDeviceID(snID)
	lunPath := o.overlaybdLoopbackDeviceLunPath(loopDevID)
	linkPath := path.Join(lunPath, "dev_"+snID)

	err = os.RemoveAll(linkPath)
	if err != nil {
		return fmt.Errorf("failed to remove loopback link %s: %w", linkPath, err)
	}

	err = os.RemoveAll(lunPath)
	if err != nil {
		return fmt.Errorf("failed to remove loopback lun %s: %w", lunPath, err)
	}

	loopDevPath := o.overlaybdLoopbackDevicePath(loopDevID)
	tpgtPath := path.Join(loopDevPath, "tpgt_1")

	err = os.RemoveAll(tpgtPath)
	if err != nil {
		return fmt.Errorf("failed to remove loopback tgpt %s: %w", tpgtPath, err)
	}

	err = os.RemoveAll(loopDevPath)
	if err != nil {
		return fmt.Errorf("failed to remove loopback dir %s: %w", loopDevPath, err)
	}

	targetPath := o.overlaybdTargetPath(snID)

	err = os.RemoveAll(targetPath)
	if err != nil {
		return fmt.Errorf("failed to remove target dir %s: %w", targetPath, err)
	}
	log.G(ctx).Infof("destroy overlaybd device success(sn: %s): %s", snID, devName)
	return nil
}

// determine whether the block device represented
// by @path is eorfs filesystem
func IsErofsFilesystem(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	byte4 := make([]byte, 4)
	_, err = f.ReadAt(byte4, 1024)
	if err != nil {
		return false
	}
	return binary.LittleEndian.Uint32(byte4) == 0xe0f5e1e2
}

// attachAndMountBlockDevice
//
// TODO(fuweid): need to track the middle state if the process has been killed.
func (o *snapshotter) attachAndMountBlockDevice(ctx context.Context, snID string, writable string, fsType string, mkfs bool) (retErr error) {

	log.G(ctx).Debugf("lookup device mountpoint(%s) if exists before attach.", snID)
	if err := lookup(o.overlaybdMountpoint(snID)); err == nil {
		return nil
	} else {
		log.G(ctx).Infof(err.Error())
	}

	targetPath := o.overlaybdTargetPath(snID)
	err := os.MkdirAll(targetPath, 0700)
	if err != nil {
		return fmt.Errorf("failed to create target dir for %s: %w", targetPath, err)
	}

	defer func() {
		if retErr != nil {
			log.G(ctx).Error(retErr.Error())
			rerr := os.RemoveAll(targetPath)
			if rerr != nil {
				log.G(ctx).WithError(rerr).Warnf("failed to clean target dir %s", targetPath)
			}
		}
	}()

	if err = os.WriteFile(path.Join(targetPath, "control"), ([]byte)(fmt.Sprintf("dev_config=overlaybd/%s", o.overlaybdConfPath(snID))), 0666); err != nil {
		return fmt.Errorf("failed to write target dev_config for %s: dev_config=overlaybd/%s: %w", targetPath, o.overlaybdConfPath(snID), err)
	}

	err = os.WriteFile(path.Join(targetPath, "control"), ([]byte)(fmt.Sprintf("max_data_area_mb=%d", obdMaxDataAreaMB)), 0666)
	if err != nil {
		return fmt.Errorf("failed to write target max_data_area_mb for %s: max_data_area_mb=%d: %w", targetPath, obdMaxDataAreaMB, err)
	}

	err = os.WriteFile(path.Join(targetPath, "enable"), ([]byte)("1"), 0666)
	if err != nil {
		// read the init-debug.log for readable
		debugLogPath := o.overlaybdInitDebuglogPath(snID)
		if data, derr := os.ReadFile(debugLogPath); derr == nil {
			return fmt.Errorf("failed to enable target for %s, %s", targetPath, data)
		}
		return fmt.Errorf("failed to enable target for %s: %w", targetPath, err)
	}

	// fixed by fuweid
	err = os.WriteFile(
		path.Join(targetPath, "attrib", "cmd_time_out"),
		([]byte)(fmt.Sprintf("%v", math.MaxInt32/1000)), 0666)
	if err != nil {
		return fmt.Errorf("failed to update cmd_time_out: %w", err)
	}

	loopDevID := o.overlaybdLoopbackDeviceID(snID)
	loopDevPath := o.overlaybdLoopbackDevicePath(loopDevID)

	err = os.MkdirAll(loopDevPath, 0700)
	if err != nil {
		return fmt.Errorf("failed to create loopback dir %s: %w", loopDevPath, err)
	}

	tpgtPath := path.Join(loopDevPath, "tpgt_1")
	lunPath := o.overlaybdLoopbackDeviceLunPath(loopDevID)
	err = os.MkdirAll(lunPath, 0700)
	if err != nil {
		return fmt.Errorf("failed to create loopback lun dir %s: %w", lunPath, err)
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
		return fmt.Errorf("failed to write loopback nexus %s: %w", nexusPath, err)
	}

	linkPath := path.Join(lunPath, "dev_"+snID)
	err = os.Symlink(targetPath, linkPath)
	if err != nil {
		return fmt.Errorf("failed to create loopback link %s: %w", linkPath, err)
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
		return fmt.Errorf("failed to read loopback address for %s: %w", devAddressPath, err)
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
			lastErr = fmt.Errorf("empty device found")
			time.Sleep(10 * time.Millisecond)
			continue
		}

		for _, dev := range devDirs {
			device := fmt.Sprintf("/dev/%s", dev.Name())

			if err := os.WriteFile(path.Join("/sys/block", dev.Name(), "device", "timeout"),
				([]byte)(fmt.Sprintf("%v", math.MaxInt32/1000)), 0666); err != nil {
				lastErr = fmt.Errorf("failed to set timeout for %s: %w", device, err)
				time.Sleep(10 * time.Millisecond)
				break // retry
			}
			devSavedPath := o.overlaybdBackstoreMarkFile(snID)
			if err := os.WriteFile(devSavedPath, []byte(device), 0644); err != nil {
				return fmt.Errorf("failed to create backstore mark file of snapshot %s: %w", snID, err)
			}
			log.G(ctx).Debugf("write device name: %s into file: %s", device, devSavedPath)

			options := strings.Split(fsType, ";")
			fstype := options[0]

			if IsErofsFilesystem(device) {
				fstype = "erofs"
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
					return fmt.Errorf("failed to mkfs for dev %s: %s: %w", device, out, err)
				}
			}

			// mount device
			if writable != RwDev {
				var mountPoint = o.overlaybdMountpoint(snID)
				mountOpts := ""
				if len(options) > 1 {
					mountOpts = options[1]
				} else {
					switch fstype {
					case "ext4":
						mountOpts = "discard"
					case "xfs":
						mountOpts = "nouuid,discard"
					default:
					}
				}
				if fstype != "ntfs" {
					var mountFlag uintptr = unix.MS_RDONLY
					if writable != RoDir {
						mountFlag = 0
					}
					log.G(ctx).Infof("fs type: %s, mount options: %s, rw: %s, mountpoint: %s",
						fstype, mountOpts, writable, mountPoint)
					if err := unix.Mount(device, mountPoint, fstype, mountFlag, mountOpts); err != nil {
						lastErr = fmt.Errorf("failed to mount %s to %s: %w", device, mountPoint, err)
						time.Sleep(10 * time.Millisecond)
						break // retry
					}
				} else {
					args := []string{"-t", fstype}
					if writable == RoDir {
						args = append(args, "-r")
					}
					if mountOpts != "" {
						args = append(args, "-o", mountOpts)
					}
					args = append(args, device, mountPoint)
					log.G(ctx).Infof("fs type: %s, mount options: %v", fstype, args)
					out, err := exec.CommandContext(ctx, "mount", args...).CombinedOutput()
					if err != nil {
						lastErr = fmt.Errorf("failed to mount for dev %s: %s: %w", device, out, err)
						time.Sleep(10 * time.Millisecond)
						break // retry
					}
				}
			}
			return nil
		}
	}
	return lastErr
}

// constructOverlayBDSpec generates the config spec for overlaybd target.
func (o *snapshotter) constructOverlayBDSpec(ctx context.Context, key string, writable bool) error {
	id, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to get info for snapshot %s: %w", key, err)
	}

	stype, err := o.identifySnapshotStorageType(ctx, id, info)
	if err != nil {
		return fmt.Errorf("failed to identify storage of snapshot %s: %w", key, err)
	}

	configJSON := sn.OverlayBDBSConfig{
		Lowers:     []sn.OverlayBDBSConfigLower{},
		ResultFile: o.overlaybdInitDebuglogPath(id),
	}

	// load the parent's config and reuse the lowerdir
	if info.Parent != "" {
		parentID, _, _, err := storage.GetInfo(ctx, info.Parent)
		if err != nil {
			return fmt.Errorf("failed to get info for parent snapshot %s: %w", info.Parent, err)
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
			return fmt.Errorf("remote block device is readonly, not support writable")
		}

		blobSize, err := strconv.Atoi(info.Labels[label.OverlayBDBlobSize])
		if err != nil {
			return fmt.Errorf("failed to parse value of label %s of snapshot %s: %w", label.OverlayBDBlobSize, key, err)
		}

		blobDigest := info.Labels[label.OverlayBDBlobDigest]
		ref, hasRef := info.Labels[label.TargetImageRef]
		if !hasRef {
			criRef, hasCriRef := info.Labels[label.CRIImageRef]
			if !hasCriRef {
				return fmt.Errorf("no image-ref label")
			}
			ref = criRef
		}

		blobPrefixURL, err := o.constructImageBlobURL(ref)
		if err != nil {
			return fmt.Errorf("failed to construct image blob prefix url for snapshot %s: %w", key, err)
		}

		configJSON.RepoBlobURL = blobPrefixURL
		if isTurboOCI, dataDgst, compType := o.checkTurboOCI(info.Labels); isTurboOCI {
			var fsmeta string

			// If parent layers exist, follow the meta choice from the bottom layer
			if info.Parent != "" {
				_, fsmeta = filepath.Split(configJSON.Lowers[0].File)
				fsmeta = filepath.Join(o.root, "snapshots", id, "fs", fsmeta)
			} else {
				fsmeta, _ = o.turboOCIFsMeta(id)
			}
			lower := sn.OverlayBDBSConfigLower{
				Dir: o.upperPath(id),
				// keep this to support ondemand turboOCI loading.
				File:         fsmeta,
				TargetDigest: dataDgst,
			}
			if isGzipLayerType(compType) {
				lower.GzipIndex = o.turboOCIGzipIdx(id)
			}
			configJSON.Lowers = append(configJSON.Lowers, lower)
		} else {
			configJSON.Lowers = append(configJSON.Lowers, sn.OverlayBDBSConfigLower{
				Digest: blobDigest,
				Size:   int64(blobSize),
				Dir:    o.upperPath(id),
			})
		}

	case storageTypeLocalBlock:
		if writable {
			return fmt.Errorf("local block device is readonly, not support writable")
		}
		ociBlob := o.overlaybdOCILayerPath(id)
		if _, err := os.Stat(ociBlob); err == nil {
			log.G(ctx).Debugf("OCI layer blob found, construct overlaybd config with turboOCI (sn: %s)", id)
			configJSON.Lowers = append(configJSON.Lowers, sn.OverlayBDBSConfigLower{
				TargetFile: o.overlaybdOCILayerPath(id),
				File:       o.magicFilePath(id),
			})
		} else {
			configJSON.Lowers = append(configJSON.Lowers, sn.OverlayBDBSConfigLower{
				Dir: o.upperPath(id),
				// automatically find overlaybd.commit
			})
		}
	case storageTypeLocalLayer:
		// 1. generate tar meta for oci layer blob
		// 2. convert local layer.tarmeta to overlaybd
		// 3. create layer's config
		var opt *utils.ConvertOption
		var rootfs_type string

		if info.Labels[label.OverlayBDBlobFsType] != "" {
			rootfs_type = info.Labels[label.OverlayBDBlobFsType]
		} else {
			rootfs_type = o.defaultFsType
		}

		if rootfs_type == "erofs" {
			opt = &utils.ConvertOption{
				TarMetaPath:    o.overlaybdOCILayerPath(id),
				Workdir:        o.convertTempdir(id),
				Ext4FSMetaPath: o.magicFilePath(id), // overlaybd.commit
				Config:         configJSON,
			}
		} else {
			log.G(ctx).Infof("generate metadata of layer blob (sn: %s)", id)
			if err := utils.GenerateTarMeta(ctx, o.overlaybdOCILayerPath(id), o.overlaybdOCILayerMeta(id)); err != nil {
				log.G(ctx).Errorf("generate tar metadata failed. (sn: %s)", id)
				return err
			}

			opt = &utils.ConvertOption{
				TarMetaPath:    o.overlaybdOCILayerMeta(id),
				Workdir:        o.convertTempdir(id),
				Ext4FSMetaPath: o.magicFilePath(id), // overlaybd.commit
				Config:         configJSON,
			}
		}
		log.G(ctx).Infof("convert layer to turboOCI (sn: %s)", id)

		if err := utils.ConvertLayer(ctx, opt, rootfs_type); err != nil {
			log.G(ctx).Error(err.Error())
			os.RemoveAll(opt.Workdir)
			os.Remove(opt.Ext4FSMetaPath)
			return err
		}

		configJSON.Lowers = append(configJSON.Lowers, sn.OverlayBDBSConfigLower{
			TargetFile: o.overlaybdOCILayerPath(id),
			File:       opt.Ext4FSMetaPath,
		})
		log.G(ctx).Debugf("generate config.json for %s:\n %+v", id, configJSON)
	default:
		if !writable {
			return fmt.Errorf("unexpect storage %v of snapshot %v during construct overlaybd spec(writable=%v, parent=%s)", stype, key, writable, info.Parent)
		}
		vsizeGB := 0
		if info.Parent == "" {
			if vsize, ok := info.Labels[label.OverlayBDVsize]; ok {
				vsizeGB, err = strconv.Atoi(vsize)
				if err != nil {
					vsizeGB = 64
				}
			} else {
				vsizeGB = 64
			}
		}
		log.G(ctx).Infof("prepare writable layer. (sn: %s, vsize: %d GB)", id, vsizeGB)
		if err := o.prepareWritableOverlaybd(ctx, id, vsizeGB); err != nil {
			return err
		}

		configJSON.Upper = sn.OverlayBDBSConfigUpper{
			Index: o.overlaybdWritableIndexPath(id),
			Data:  o.overlaybdWritableDataPath(id),
		}
	}

	if isTurboOCI, _, _ := o.checkTurboOCI(info.Labels); isTurboOCI {
		// If the fallback file exists, enforce TurboOCI fstype to EXT4
		ext4FSFallbackPath := filepath.Join(o.root, ext4FSFallbackFile)
		_, err = os.Stat(ext4FSFallbackPath)
		if err == nil && configJSON.Lowers[0].File != "" {
			var newLowers []sn.OverlayBDBSConfigLower
			log.G(ctx).Infof("fallback to EXT4 since %s exists", ext4FSFallbackPath)
			for _, l := range configJSON.Lowers {
				s, _ := filepath.Split(l.File)
				l.File = filepath.Join(s, "ext4.fs.meta")
				newLowers = append(newLowers, l)
			}
			configJSON.Lowers = newLowers
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
	accelLayerLower := sn.OverlayBDBSConfigLower{Dir: o.upperPath(id)}
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
func (o *snapshotter) loadBackingStoreConfig(snID string) (*sn.OverlayBDBSConfig, error) {
	confPath := o.overlaybdConfPath(snID)
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config(path=%s) of snapshot %s: %w", confPath, snID, err)
	}

	var configJSON sn.OverlayBDBSConfig
	if err := json.Unmarshal(data, &configJSON); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data(%s): %w", string(data), err)
	}

	return &configJSON, nil
}

// constructImageBlobURL returns the https://host/v2/<name>/blobs/.
//
// TODO(fuweid): How to know the existing url schema?
func (o *snapshotter) constructImageBlobURL(ref string) (string, error) {
	refspec, err := reference.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("invalid repo url %s: %w", ref, err)
	}

	host := refspec.Hostname()
	repo := strings.TrimPrefix(refspec.Locator, host+"/")
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	scheme := "https://"
	for _, reg := range o.mirrorRegistry {
		if host == reg.Host && reg.Insecure {
			scheme = "http://"
		}
	}
	return scheme + path.Join(host, "v2", repo) + "/blobs", nil
}

// atomicWriteOverlaybdTargetConfig
func (o *snapshotter) atomicWriteOverlaybdTargetConfig(snID string, configJSON *sn.OverlayBDBSConfig) error {
	data, err := json.Marshal(configJSON)
	if err != nil {
		return fmt.Errorf("failed to marshal %+v configJSON into JSON: %w", configJSON, err)
	}

	confPath := o.overlaybdConfPath(snID)
	if err := continuity.AtomicWriteFile(confPath, data, 0600); err != nil {
		return fmt.Errorf("failed to commit the overlaybd config on %s: %w", confPath, err)
	}
	return nil
}

// prepareWritableOverlaybd
func (o *snapshotter) prepareWritableOverlaybd(ctx context.Context, snID string, vsizeGB int) error {
	args := []string{fmt.Sprintf("%d", vsizeGB)}
	if o.writableLayerType == "sparse" {
		args = append(args, "-s")
	}
	return utils.Create(ctx, o.blockPath(snID), args...)
}

func (o *snapshotter) sealWritableOverlaybd(ctx context.Context, snID string) (retErr error) {
	return utils.Seal(ctx, o.blockPath(snID), o.upperPath(snID))
}

func (o *snapshotter) obdHbaNum() int {
	if o.tenant == -1 {
		return obdHbaNum
	}
	return o.tenant
}

func (o *snapshotter) obdLoopNaaPrefix() int {
	if o.tenant == -1 {
		return obdLoopNaaPrefix
	}
	return o.tenant%100 + 100 // keep first num is '1'
}

func (o *snapshotter) overlaybdTargetPath(id string) string {
	return fmt.Sprintf("/sys/kernel/config/target/core/user_%d/dev_%s", o.obdHbaNum(), id)
}

func (o *snapshotter) overlaybdLoopbackDeviceID(id string) string {
	paddings := strings.Repeat("0", 13-len(id))
	return fmt.Sprintf("naa.%03d%s%s", o.obdLoopNaaPrefix(), paddings, id)
}

func (o *snapshotter) overlaybdLoopbackDevicePath(id string) string {
	return fmt.Sprintf("/sys/kernel/config/target/loopback/%s", id)
}

func (o *snapshotter) overlaybdLoopbackDeviceLunPath(id string) string {
	return fmt.Sprintf("%s/tpgt_1/lun/lun_0", o.overlaybdLoopbackDevicePath(id))
}

func (o *snapshotter) scsiBlockDevicePath(deviceNumber string) string {
	return fmt.Sprintf("/sys/class/scsi_device/%s:0/device/block", deviceNumber)
}

// TODO: use device number to check?
func lookup(dir string) error {
	dir = filepath.Clean(dir)

	m, err := mountinfo.GetMounts(mountinfo.SingleEntryFilter(dir))
	if err != nil {
		return fmt.Errorf("failed to get mount info for %q: %w", dir, err)
	}

	if len(m) == 0 {
		return fmt.Errorf("failed to find the mount point for %q", dir)
	}
	return nil
}

func isGzipLayerType(mediaType string) bool {
	return mediaType == specs.MediaTypeImageLayerGzip || mediaType == images.MediaTypeDockerSchema2LayerGzip
}

func (o *snapshotter) diskUsageWithBlock(ctx context.Context, id string, stype storageType) (snapshots.Usage, error) {
	usage := snapshots.Usage{}
	du, err := fs.DiskUsage(ctx, o.upperPath(id))
	if err != nil {
		return snapshots.Usage{}, err
	}
	usage = snapshots.Usage(du)
	if stype == storageTypeRemoteBlock || stype == storageTypeLocalBlock {
		du, err := utils.DiskUsageWithoutMountpoint(ctx, o.blockPath(id))
		if err != nil {
			return snapshots.Usage{}, err
		}
		usage.Add(snapshots.Usage(du))
	}
	return usage, nil
}
