package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/iscsi"

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
	// tgt related
	tgtBackingStoreOverlayBD = "overlaybd"

	defaultInitiatorAddress = "127.0.0.1"
	defaultInitiatorPort    = "3260"
	defaultPortal           = defaultInitiatorAddress + ":" + defaultInitiatorPort

	maxAttachAttempts      = 10
	defaultRollbackTimeout = 30 * time.Second
)

// OverlayBDBSConfig is the config of OverlayBD backing store in open-iscsi target.
type OverlayBDBSConfig struct {
	RepoBlobURL string                   `json:"repoBlobUrl"`
	Lowers      []OverlayBDBSConfigLower `json:"lowers"`
	Upper       OverlayBDBSConfigUpper   `json:"upper"`
	ResultFile  string                   `json:"resultFile"`
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
	mountPoint := o.tgtTargetMountpoint(snID)
	if err := mount.UnmountAll(mountPoint, 0); err != nil {
		return errors.Wrapf(err, "failed to umount %s", mountPoint)
	}

	// logout portal
	targetIqn := o.tgtTargetIqn(snID, snKey)
	out, err := exec.CommandContext(ctx, "iscsiadm", "-m", "node", "-p", defaultPortal, "-T", targetIqn, "--logout").CombinedOutput()
	if err != nil {
		exiterr, ok := err.(*exec.ExitError)
		if !ok || iscsi.Errno(exiterr.ExitCode()) != iscsi.ENOOBJSFOUND {
			return errors.Wrapf(err, "failed to logout a portal on a target %s: %s", targetIqn, out)
		}
	}

	// delete the portal
	out, err = exec.CommandContext(ctx, "iscsiadm", "-m", "node", "-p", defaultPortal, "-T", targetIqn, "-o", "delete").CombinedOutput()
	if err != nil {
		exiterr, ok := err.(*exec.ExitError)
		if !ok || iscsi.Errno(exiterr.ExitCode()) != iscsi.ENOOBJSFOUND {
			return errors.Wrapf(err, "failed to delete a portal on a target %s: %s", targetIqn, out)
		}
	}

	// delete the target
	out, err = exec.CommandContext(ctx, "tgt-admin", "--delete", targetIqn).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to delete target %s: %s", targetIqn, out)
	}
	return nil
}

// attachAndMountBlockDevice
//
// TODO(fuweid): need to track the middle state if the process has been killed.
func (o *snapshotter) attachAndMountBlockDevice(ctx context.Context, snID string, snKey string, writable bool) (retErr error) {
	if err := lookup(o.tgtTargetMountpoint(snID)); err == nil {
		return nil
	}

	// If the target already exists, it won't be processed, see man TGT-ADMIN(8)
	targetConfPath := o.tgtTargetConfPath(snID, snKey)
	out, err := exec.CommandContext(ctx, "tgt-admin", "-e", "-c", targetConfPath).CombinedOutput()
	if err != nil {
		// read the init-debug.log for readable
		debugLogPath := o.tgtOverlayBDInitDebuglogPath(snID)
		if data, derr := ioutil.ReadFile(debugLogPath); derr == nil {
			return errors.Wrapf(err, "failed to create target by tgt-admin: %s, more detail in %s", out, data)
		}
		return errors.Wrapf(err, "failed to create target by tgt-admin: %s", out)
	}

	targetIqn := o.tgtTargetIqn(snID, snKey)
	defer func() {
		if retErr != nil {
			deferCtx, deferCancel := rollbackContext()
			defer deferCancel()

			out, err = exec.CommandContext(ctx, "tgt-admin", "--delete", targetIqn).CombinedOutput()
			if err != nil {
				log.G(deferCtx).WithError(err).Warnf("failed to rollback target by tgt-admin: %s", out)
			}
		}
	}()

	// Add a portal on a target
	out, err = exec.CommandContext(ctx, "iscsiadm", "-m", "node", "-p", defaultPortal, "-T", targetIqn, "-o", "new").CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to add a portal on a target %s: %s", targetIqn, out)
	}
	defer func() {
		// rollback the portal
		if retErr != nil {
			deferCtx, deferCancel := rollbackContext()
			defer deferCancel()

			out, err = exec.CommandContext(deferCtx, "iscsiadm", "-m", "node", "-p", defaultPortal, "-T", targetIqn, "-o", "delete").CombinedOutput()
			if err != nil {
				log.G(deferCtx).WithError(err).Warnf("failed to rollback a portal on a target %s: %s", targetIqn, out)
			}
		}
	}()

	// Login a portal on a target
	out, err = exec.CommandContext(ctx, "iscsiadm", "-m", "node", "-p", defaultPortal, "-T", targetIqn, "--login").CombinedOutput()
	if err != nil {
		exiterr, ok := err.(*exec.ExitError)
		if !ok || iscsi.Errno(exiterr.ExitCode()) != iscsi.ESESSEXISTS {
			return errors.Wrapf(err, "failed to login a portal on a target %s: %s", targetIqn, out)
		}
	}
	defer func() {
		// NOTE(fuweid): Basically, do login only once. The rollback doesn't impact other running portal.
		if retErr != nil {
			deferCtx, deferCancel := rollbackContext()
			defer deferCancel()

			out, err = exec.CommandContext(deferCtx, "iscsiadm", "-m", "node", "-p", defaultPortal, "-T", targetIqn, "--logout").CombinedOutput()
			if err != nil {
				log.G(deferCtx).WithError(err).Warnf("failed to rollback to logout on a target %s: %s", targetIqn, out)
			}
		}
	}()

	// Find the session and hostNumber mapping
	hostToSessionID, err := iscsi.GetISCSIHostSessionMapForTarget(targetIqn, defaultPortal)
	if err != nil {
		return errors.Wrapf(err, "failed to get hostNumber->SessionID mapping for %s", targetIqn)
	}

	if len(hostToSessionID) != 1 {
		return errors.Errorf("unexpected hostNumber->SessionID mapping result %v for %s", hostToSessionID, targetIqn)
	}

	// The device doesn't show up instantly. Need retry here.
	var lastErr error = nil
	var mountPoint = o.tgtTargetMountpoint(snID)
	for i := 1; i <= maxAttachAttempts; i++ {
		for hostNumber, sessionIDs := range hostToSessionID {
			if len(sessionIDs) != 1 {
				return errors.Errorf("unexpected hostNumber->SessionID mapping result %v for %s", hostToSessionID, targetIqn)
			}

			// Assume that both channelID and targetID are zero.
			devices, err := iscsi.GetDevicesForTarget(targetIqn, hostNumber, sessionIDs[0], 0, 0)
			if err != nil {
				return err
			}

			if len(devices) != 1 {
				lastErr = errors.Errorf("unexpected devices %v for %s", devices, targetIqn)
				break
			}

			var mflag uintptr = unix.MS_RDONLY
			if writable {
				mflag = 0
			}

			// TODO(fuweid): how to support multiple filesystem?
			if err := unix.Mount(devices[0], mountPoint, "ext4", mflag, ""); err != nil {
				return errors.Wrapf(err, "failed to mount the device %s on %s", devices[0], mountPoint)
			}
			lastErr = nil
		}

		if lastErr == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	return lastErr
}

// constructOverlayBDSpec generates the config spec for OverlayBD backing store.
func (o *snapshotter) constructOverlayBDSpec(ctx context.Context, key string, writable bool) error {
	id, info, _, err := storage.GetInfo(ctx, key)
	if err != nil {
		return errors.Wrapf(err, "failed to get info for snapshot %s", key)
	}

	stype, err := o.identifySnapshotStorageType(id, info)
	if err != nil {
		return errors.Wrapf(err, "failed to identify storage of snapshot %s", key)
	}

	configJSON := OverlayBDBSConfig{
		Lowers:     []OverlayBDBSConfigLower{},
		ResultFile: o.tgtOverlayBDInitDebuglogPath(id),
	}

	// load the parent's config and reuse the lowerdir
	if info.Parent != "" {
		parentConfJSON, err := o.loadBackingStoreConfig(ctx, info.Parent)
		if err != nil {
			return err
		}
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
		blobPrefixURL, err := o.constructImageBlobURL(info.Labels[labelKeyImageRef])
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
		if !writable || info.Parent == "" {
			return errors.Errorf("unexpect storage %v of snapshot %v during construct overlaybd spec(writable=%v, parent=%s)", stype, key, writable, info.Parent)
		}

		if err := o.prepareWritableOverlaybd(ctx, id); err != nil {
			return err
		}

		configJSON.Upper = OverlayBDBSConfigUpper{
			Index: o.tgtOverlayBDWritableIndexPath(id),
			Data:  o.tgtOverlayBDWritableDataPath(id),
		}
	}
	return o.atomicWriteBackingStoreAndTargetConfig(ctx, id, key, configJSON)
}

// loadBackingStoreConfig loads OverlayBD backing store config.
func (o *snapshotter) loadBackingStoreConfig(ctx context.Context, snKey string) (*OverlayBDBSConfig, error) {
	id, _, _, err := storage.GetInfo(ctx, snKey)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get info of snapshot %s", snKey)
	}

	confPath := o.tgtOverlayBDConfPath(id)
	data, err := ioutil.ReadFile(confPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read config(path=%s) of snapshot %s", confPath, snKey)
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

// atomicWriteBackingStoreAndTargetConfig
func (o *snapshotter) atomicWriteBackingStoreAndTargetConfig(ctx context.Context, snID string, snKey string, configJSON OverlayBDBSConfig) error {
	data, err := json.Marshal(configJSON)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal %+v configJSON into JSON", configJSON)
	}

	confPath := o.tgtOverlayBDConfPath(snID)
	if err := continuity.AtomicWriteFile(confPath, data, 0600); err != nil {
		return errors.Wrapf(err, "failed to commit the OverlayBD config on %s", confPath)
	}

	confDataStr := generateTargetConfInXML(o.tgtTargetIqn(snID, snKey), confPath)
	targetConfPath := o.tgtTargetConfPath(snID, snKey)
	return errors.Wrapf(continuity.AtomicWriteFile(targetConfPath, []byte(confDataStr), 0600),
		"failed to commit the target config on %s", targetConfPath)
}

// prepareWritableOverlaybd
func (o *snapshotter) prepareWritableOverlaybd(ctx context.Context, snID string) error {
	binpath := filepath.Join(o.config.OverlayBDUtilBinDir, "overlaybd-create")

	// TODO(fuweid): 256GB can be configurable?
	out, err := exec.CommandContext(ctx, binpath,
		o.tgtOverlayBDWritableDataPath(snID),
		o.tgtOverlayBDWritableIndexPath(snID), "256").CombinedOutput()
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
		o.tgtOverlayBDWritableDataPath(snID),
		o.tgtOverlayBDWritableIndexPath(snID), tmpPath).CombinedOutput()
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

// generateTargetConfInXML
func generateTargetConfInXML(targetIqn string, configPath string) string {
	const fmtTargetConf = `<target %q>
   <backing-store %s>
      bs-type %s
   </backing-store>
   initiator-address %s
</target>
`
	return fmt.Sprintf(fmtTargetConf, targetIqn, configPath, tgtBackingStoreOverlayBD, defaultInitiatorAddress)
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

func rollbackContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.TODO(), defaultRollbackTimeout)
}
