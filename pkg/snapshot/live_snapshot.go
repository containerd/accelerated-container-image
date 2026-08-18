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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/storage"
	"github.com/containerd/errdefs"
)

const liveSnapshotMetadataSchema = 1

// liveSnapshotMetadata is persisted under the native Docker owner snapshot
// directory and reused across snapshotter/tcmu/Docker restarts.
type liveSnapshotMetadata struct {
	Schema          int    `json:"schema"`
	OwnerSnapshotID string `json:"owner_snapshot_id"`
	DeviceID        string `json:"device_id"`
	ConfigPath      string `json:"config_path"`
	Mountpoint      string `json:"mountpoint"`
	BlockDevicePath string `json:"block_device_path,omitempty"`
}

func (o *snapshotter) liveSnapshotMetadataPath(snID string) string {
	return filepath.Join(o.snPath(snID), "block", "live-snapshot.json")
}

func newLiveSnapshotDeviceID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (o *snapshotter) loadLiveSnapshotMetadata(snID string) (*liveSnapshotMetadata, error) {
	path := o.liveSnapshotMetadataPath(snID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var meta liveSnapshotMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("corrupt live-snapshot metadata: %w", err)
	}
	if meta.Schema != liveSnapshotMetadataSchema {
		return nil, fmt.Errorf("unsupported live-snapshot metadata schema %d", meta.Schema)
	}
	if meta.DeviceID == "" || meta.OwnerSnapshotID == "" || meta.ConfigPath == "" || meta.Mountpoint == "" {
		return nil, fmt.Errorf("incomplete live-snapshot metadata")
	}
	if meta.OwnerSnapshotID != snID {
		return nil, fmt.Errorf("live-snapshot metadata owner mismatch")
	}
	return &meta, nil
}

func (o *snapshotter) storeLiveSnapshotMetadata(meta *liveSnapshotMetadata) error {
	if meta == nil {
		return fmt.Errorf("nil live-snapshot metadata")
	}
	dir := filepath.Dir(o.liveSnapshotMetadataPath(meta.OwnerSnapshotID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := o.liveSnapshotMetadataPath(meta.OwnerSnapshotID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, o.liveSnapshotMetadataPath(meta.OwnerSnapshotID))
}

func (o *snapshotter) ensureLiveSnapshotMetadata(snID string) (*liveSnapshotMetadata, error) {
	if meta, err := o.loadLiveSnapshotMetadata(snID); err == nil {
		return meta, nil
	} else if !os.IsNotExist(err) {
		// Fail closed on corrupt/conflicting metadata.
		return nil, err
	}

	devID, err := newLiveSnapshotDeviceID()
	if err != nil {
		return nil, err
	}
	meta := &liveSnapshotMetadata{
		Schema:          liveSnapshotMetadataSchema,
		OwnerSnapshotID: snID,
		DeviceID:        devID,
		ConfigPath:      o.overlaybdConfPath(snID),
		Mountpoint:      o.overlaybdMountpoint(snID),
	}
	if err := o.storeLiveSnapshotMetadata(meta); err != nil {
		return nil, err
	}
	return meta, nil
}

func isRuntimeOwnedOverlayBDLabel(key string) bool {
	switch key {
	case label.OverlayBDDeviceID,
		label.OverlayBDConfigPath,
		label.OverlayBDDeviceOwner,
		label.OverlayBDNativeBaseSnapshot,
		label.OverlayBDLiveSnapshot:
		return true
	default:
		return false
	}
}

func isLiveSnapshotLabeled(info snapshots.Info) bool {
	return info.Labels[label.OverlayBDLiveSnapshot] == "true"
}

// copyRuntimeOwnedOverlayBDLabels returns the runtime-owned OverlayBD labels
// that must survive CommitActive. Moby Commit opts use snapshots.WithLabels,
// which replaces the entire label map and would otherwise drop these.
func copyRuntimeOwnedOverlayBDLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range labels {
		if isRuntimeOwnedOverlayBDLabel(key) {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// withPreservedRuntimeOwnedLabels merges runtime-owned OverlayBD labels into
// the committed snapshot info after any WithLabels opt has replaced the map.
func withPreservedRuntimeOwnedLabels(preserved map[string]string) snapshots.Opt {
	return func(info *snapshots.Info) error {
		if len(preserved) == 0 {
			return nil
		}
		if info.Labels == nil {
			info.Labels = map[string]string{}
		}
		for key, value := range preserved {
			info.Labels[key] = value
		}
		return nil
	}
}

// isLiveSnapshotOwner reports whether snapshot id owns the Docker-native
// writable OverlayBD device (collapsed init).
func isLiveSnapshotOwner(id string, info snapshots.Info) bool {
	return isLiveSnapshotLabeled(info) && info.Labels[label.OverlayBDDeviceOwner] == id
}

// isLiveSnapshotAlias reports whether snapshot id aliases another snapshot's
// live-snapshot device rather than owning one.
func isLiveSnapshotAlias(id string, info snapshots.Info) bool {
	owner := info.Labels[label.OverlayBDDeviceOwner]
	return isLiveSnapshotLabeled(info) && owner != "" && owner != id
}

// rejectRuntimeOwnedLabelMutation rejects client Update attempts that change
// or remove runtime-owned live-snapshot labels. Matching values are allowed.
func rejectRuntimeOwnedLabelMutation(current, update snapshots.Info, fieldpaths ...string) error {
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	if update.Labels == nil {
		update.Labels = map[string]string{}
	}

	checkKey := func(key string) error {
		if !isRuntimeOwnedOverlayBDLabel(key) {
			return nil
		}
		cur, curOK := current.Labels[key]
		next, nextOK := update.Labels[key]
		if !curOK && !nextOK {
			return nil
		}
		if curOK && nextOK && cur == next {
			return nil
		}
		if curOK && !nextOK {
			return fmt.Errorf("cannot remove runtime-owned label %q: %w", key, errdefs.ErrInvalidArgument)
		}
		if !curOK && nextOK {
			return fmt.Errorf("cannot set runtime-owned label %q: %w", key, errdefs.ErrInvalidArgument)
		}
		return fmt.Errorf("cannot mutate runtime-owned label %q: %w", key, errdefs.ErrInvalidArgument)
	}

	if len(fieldpaths) == 0 {
		for key := range current.Labels {
			if err := checkKey(key); err != nil {
				return err
			}
		}
		for key := range update.Labels {
			if err := checkKey(key); err != nil {
				return err
			}
		}
		return nil
	}

	for _, path := range fieldpaths {
		switch {
		case path == "labels":
			for key := range current.Labels {
				if err := checkKey(key); err != nil {
					return err
				}
			}
			for key := range update.Labels {
				if err := checkKey(key); err != nil {
					return err
				}
			}
		case strings.HasPrefix(path, "labels."):
			if err := checkKey(strings.TrimPrefix(path, "labels.")); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureSingleLiveSnapshotChild rejects a second active/view alias against the
// same private Docker init owner. excludeKey is the snapshot currently being
// prepared and is ignored if already present.
func ensureSingleLiveSnapshotChild(ctx context.Context, ownerKey, excludeKey string) error {
	var conflict string
	if err := storage.WalkInfo(ctx, func(ctx context.Context, info snapshots.Info) error {
		if conflict != "" || info.Name == excludeKey || info.Parent != ownerKey {
			return nil
		}
		if info.Kind != snapshots.KindActive && info.Kind != snapshots.KindView {
			return nil
		}
		if info.Labels[label.OverlayBDLiveSnapshot] != "true" {
			return nil
		}
		conflict = info.Name
		return nil
	}); err != nil {
		return err
	}
	if conflict != "" {
		return fmt.Errorf("live-snapshot owner %q already has child %q: %w", ownerKey, conflict, errdefs.ErrFailedPrecondition)
	}
	return nil
}
