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
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
)

func TestIsRuntimeOwnedOverlayBDLabel(t *testing.T) {
	owned := []string{
		label.OverlayBDDeviceID,
		label.OverlayBDConfigPath,
		label.OverlayBDDeviceOwner,
		label.OverlayBDNativeBaseSnapshot,
		label.OverlayBDLiveSnapshot,
	}
	for _, key := range owned {
		if !isRuntimeOwnedOverlayBDLabel(key) {
			t.Errorf("expected %q to be runtime-owned", key)
		}
	}
	if isRuntimeOwnedOverlayBDLabel(label.SupportReadWriteMode) {
		t.Errorf("SupportReadWriteMode should not be treated as a protected live-snapshot label")
	}
}

func TestRejectRuntimeOwnedLabelMutation(t *testing.T) {
	current := snapshots.Info{
		Name: "sn",
		Labels: map[string]string{
			label.OverlayBDLiveSnapshot: "true",
			label.OverlayBDDeviceID:     "abc",
			"user.label":                "keep",
		},
	}

	t.Run("identical full replace allowed", func(t *testing.T) {
		update := snapshots.Info{
			Name: "sn",
			Labels: map[string]string{
				label.OverlayBDLiveSnapshot: "true",
				label.OverlayBDDeviceID:     "abc",
				"user.label":                "keep",
			},
		}
		if err := rejectRuntimeOwnedLabelMutation(current, update); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("mutate device id rejected", func(t *testing.T) {
		update := snapshots.Info{
			Name: "sn",
			Labels: map[string]string{
				label.OverlayBDLiveSnapshot: "true",
				label.OverlayBDDeviceID:     "mutated",
				"user.label":                "keep",
			},
		}
		err := rejectRuntimeOwnedLabelMutation(current, update)
		if !errdefs.IsInvalidArgument(err) {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})

	t.Run("remove live-snapshot flag rejected", func(t *testing.T) {
		update := snapshots.Info{
			Name: "sn",
			Labels: map[string]string{
				label.OverlayBDDeviceID: "abc",
				"user.label":            "keep",
			},
		}
		err := rejectRuntimeOwnedLabelMutation(current, update, "labels")
		if !errdefs.IsInvalidArgument(err) {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})

	t.Run("fieldpath mutate rejected", func(t *testing.T) {
		update := snapshots.Info{
			Name: "sn",
			Labels: map[string]string{
				label.OverlayBDDeviceID: "mutated",
			},
		}
		err := rejectRuntimeOwnedLabelMutation(current, update, "labels."+label.OverlayBDDeviceID)
		if !errdefs.IsInvalidArgument(err) {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})

	t.Run("unrelated label update allowed", func(t *testing.T) {
		update := snapshots.Info{
			Name: "sn",
			Labels: map[string]string{
				"user.label": "new",
			},
		}
		if err := rejectRuntimeOwnedLabelMutation(current, update, "labels.user.label"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("client cannot invent runtime label", func(t *testing.T) {
		bare := snapshots.Info{Name: "sn", Labels: map[string]string{"user.label": "x"}}
		update := snapshots.Info{
			Name: "sn",
			Labels: map[string]string{
				"user.label":                "x",
				label.OverlayBDLiveSnapshot: "true",
			},
		}
		err := rejectRuntimeOwnedLabelMutation(bare, update)
		if !errdefs.IsInvalidArgument(err) {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})
}

func TestLiveSnapshotOwnerAliasHelpers(t *testing.T) {
	owner := snapshots.Info{
		Labels: map[string]string{
			label.OverlayBDLiveSnapshot: "true",
			label.OverlayBDDeviceOwner:  "7",
			label.OverlayBDDeviceID:     "dev",
		},
	}
	alias := snapshots.Info{
		Labels: map[string]string{
			label.OverlayBDLiveSnapshot: "true",
			label.OverlayBDDeviceOwner:  "7",
			label.OverlayBDDeviceID:     "dev",
		},
	}
	if !isLiveSnapshotOwner("7", owner) {
		t.Fatal("expected owner helper to match numeric id")
	}
	if isLiveSnapshotAlias("7", owner) {
		t.Fatal("owner must not be classified as alias")
	}
	if !isLiveSnapshotAlias("9", alias) {
		t.Fatal("expected alias helper to match foreign owner")
	}
	if isLiveSnapshotOwner("9", alias) {
		t.Fatal("alias must not be classified as owner")
	}
}

func TestEnsureLiveSnapshotMetadataCreateReuseCorrupt(t *testing.T) {
	root := t.TempDir()
	o := &snapshotter{root: root}
	snID := "42"
	if err := os.MkdirAll(filepath.Join(root, "snapshots", snID, "block"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	meta, err := o.ensureLiveSnapshotMetadata(snID)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if meta.DeviceID == "" || len(meta.DeviceID) != 64 {
		t.Fatalf("expected 256-bit hex device id, got %q", meta.DeviceID)
	}
	if meta.OwnerSnapshotID != snID {
		t.Fatalf("owner mismatch: %q", meta.OwnerSnapshotID)
	}
	st, err := os.Stat(o.liveSnapshotMetadataPath(snID))
	if err != nil {
		t.Fatalf("stat metadata: %v", err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("expected mode 0600, got %o", st.Mode().Perm())
	}

	again, err := o.ensureLiveSnapshotMetadata(snID)
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if again.DeviceID != meta.DeviceID {
		t.Fatalf("device id rotated on reuse: %q -> %q", meta.DeviceID, again.DeviceID)
	}

	if err := os.WriteFile(o.liveSnapshotMetadataPath(snID), []byte("{"), 0600); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}
	if _, err := o.ensureLiveSnapshotMetadata(snID); err == nil {
		t.Fatal("expected corrupt metadata to fail closed")
	}
}

func TestNewSnapshotterDockerWritableModeValidation(t *testing.T) {
	cfg := DefaultBootConfig()
	cfg.Root = t.TempDir()
	cfg.RuntimeType = "containerd"
	cfg.DockerWritableMode = DockerWritableNative
	if _, err := NewSnapshotter(cfg); err == nil {
		t.Fatal("expected native mode without docker runtime to fail")
	}

	cfg = DefaultBootConfig()
	cfg.Root = t.TempDir()
	cfg.RuntimeType = "docker"
	cfg.DockerWritableMode = "bogus"
	if _, err := NewSnapshotter(cfg); err == nil {
		t.Fatal("expected bogus dockerWritableMode to fail")
	}

	cfg = DefaultBootConfig()
	cfg.Root = t.TempDir()
	cfg.RuntimeType = "docker"
	cfg.DockerWritableMode = DockerWritableNative
	sn, err := NewSnapshotter(cfg)
	if err != nil {
		t.Fatalf("NewSnapshotter native docker: %v", err)
	}
	defer sn.Close()
}

func TestCopyRuntimeOwnedOverlayBDLabels(t *testing.T) {
	labels := map[string]string{
		label.OverlayBDLiveSnapshot:      "true",
		label.OverlayBDDeviceID:          "dev",
		label.OverlayBDConfigPath:        "/cfg",
		label.OverlayBDDeviceOwner:       "2",
		label.OverlayBDNativeBaseSnapshot: "1",
		label.SupportReadWriteMode:       "dir",
		"user.label":                     "keep-out",
	}
	got := copyRuntimeOwnedOverlayBDLabels(labels)
	want := map[string]string{
		label.OverlayBDLiveSnapshot:       "true",
		label.OverlayBDDeviceID:           "dev",
		label.OverlayBDConfigPath:         "/cfg",
		label.OverlayBDDeviceOwner:        "2",
		label.OverlayBDNativeBaseSnapshot: "1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key %s: got %q want %q", k, got[k], v)
		}
	}
}

func TestWithPreservedRuntimeOwnedLabelsSurvivesWithLabelsReplace(t *testing.T) {
	preserved := map[string]string{
		label.OverlayBDLiveSnapshot: "true",
		label.OverlayBDDeviceID:     "dev",
		label.OverlayBDDeviceOwner:  "2",
	}
	info := snapshots.Info{
		Labels: map[string]string{
			label.OverlayBDLiveSnapshot: "true",
			label.OverlayBDDeviceID:     "dev",
			label.OverlayBDDeviceOwner:  "2",
			"other":                     "x",
		},
	}
	// Simulate Moby Commit: WithLabels replaces the entire map.
	if err := snapshots.WithLabels(map[string]string{"moby": "1"})(&info); err != nil {
		t.Fatalf("WithLabels: %v", err)
	}
	if err := withPreservedRuntimeOwnedLabels(preserved)(&info); err != nil {
		t.Fatalf("preserve: %v", err)
	}
	if info.Labels[label.OverlayBDLiveSnapshot] != "true" || info.Labels[label.OverlayBDDeviceID] != "dev" {
		t.Fatalf("runtime labels not preserved: %#v", info.Labels)
	}
	if info.Labels["moby"] != "1" {
		t.Fatalf("moby labels should remain: %#v", info.Labels)
	}
}

func TestShouldSkipSealOnLiveSnapshotCommit(t *testing.T) {
	cases := []struct {
		name string
		info snapshots.Info
		seal bool
	}{
		{
			name: "ordinary writable seals",
			info: snapshots.Info{Labels: map[string]string{label.SupportReadWriteMode: "dir"}},
			seal: true,
		},
		{
			name: "live-snapshot skips seal",
			info: snapshots.Info{Labels: map[string]string{
				label.SupportReadWriteMode:  "dir",
				label.OverlayBDLiveSnapshot: "true",
			}},
			seal: false,
		},
		{
			name: "no writable label skips seal path",
			info: snapshots.Info{Labels: map[string]string{}},
			seal: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, writable := tc.info.Labels[label.SupportReadWriteMode]
			shouldSeal := writable && !isLiveSnapshotLabeled(tc.info)
			if shouldSeal != tc.seal {
				t.Fatalf("shouldSeal=%v want %v", shouldSeal, tc.seal)
			}
		})
	}
}
