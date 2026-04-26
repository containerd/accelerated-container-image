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
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/pkg/testutil"
)

// TestIsDockerInitLayer covers the pure helper used to detect
// Docker "-init" layers for both the committed form ("foo-init")
// and the active prepare-key form ("foo-init-key").
func TestIsDockerInitLayer(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want bool
	}{
		{"committed init suffix", "k8s.io/1/somesn-init", true},
		{"active init prepare key", "k8s.io/1/somesn-init-key", true},
		{"plain layer key", "k8s.io/1/somesn", false},
		{"empty key", "", false},
		{"init in the middle only", "random-init-random", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDockerInitLayer(tc.key); got != tc.want {
				t.Errorf("IsDockerInitLayer(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestIsDockerContainerLayer covers the pure helper used to detect
// that the given parent key is a Docker init layer (i.e. current
// snapshot is a Docker container rootfs layer).
func TestIsDockerContainerLayer(t *testing.T) {
	cases := []struct {
		name   string
		parent string
		want   bool
	}{
		{"parent is committed init", "k8s.io/1/somesn-init", true},
		{"parent empty", "", false},
		{"parent is regular image layer", "sha256:abc/layer1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDockerContainerLayer(tc.parent); got != tc.want {
				t.Errorf("IsDockerContainerLayer(%q) = %v, want %v", tc.parent, got, tc.want)
			}
		})
	}
}

// TestSnapshotterDockerGates makes sure the snapshotter methods
// honour both the runtimeType and rwMode gates, so Docker-specific
// behaviour only kicks in under runtimeType="docker" AND rwMode=RoDir.
func TestSnapshotterDockerGates(t *testing.T) {
	cases := []struct {
		name        string
		runtimeType string
		rwMode      string
		key         string
		parent      string
		wantInit    bool
		wantCont    bool
	}{
		{"docker + overlayfs", "docker", RoDir, "foo-init-key", "foo-init", true, true},
		{"docker + RwDir (gated off)", "docker", RwDir, "foo-init-key", "foo-init", false, false},
		{"containerd runtime (gated off)", "containerd", RoDir, "foo-init-key", "foo-init", false, false},
		{"docker + non-init key", "docker", RoDir, "foo", "bar", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &snapshotter{runtimeType: tc.runtimeType, rwMode: tc.rwMode}
			if got := o.isDockerInitLayer(tc.key); got != tc.wantInit {
				t.Errorf("isDockerInitLayer = %v, want %v", got, tc.wantInit)
			}
			if got := o.isDockerContainerLayer(tc.parent); got != tc.wantCont {
				t.Errorf("isDockerContainerLayer = %v, want %v", got, tc.wantCont)
			}
		})
	}
}

// TestDockerContainerLayerMountNormalImage drives the full snapshotter
// flow in Docker runtime mode over a normal (non-overlaybd) image, and
// asserts that Mounts on the container layer returns a non-nil standard
// overlay mount (regression guard for the fix in pkg/snapshot/docker.go
// which previously returned a nil mount for normal images).
//
// Flow:
//  1. Prepare + Commit a normal image top layer.
//  2. Prepare + Commit a Docker init layer whose parent is the image top.
//  3. Prepare a Docker container layer whose parent is the init layer.
//  4. Call Mounts on the container layer and validate the returned
//     overlay mount has upper/work/lower dirs correctly composed.
func TestDockerContainerLayerMountNormalImage(t *testing.T) {
	testutil.RequiresRoot(t)

	ctx := context.Background()
	root := t.TempDir()

	cfg := DefaultBootConfig()
	cfg.Root = root
	cfg.RuntimeType = "docker"
	// rwMode defaults to "overlayfs" (RoDir), which is the mode the
	// Docker init/container handling is gated on.

	sn, err := NewSnapshotter(cfg)
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}
	defer sn.Close()

	// 1. Normal image top layer.
	const (
		imgKey  = "test/image-layer-key"
		imgName = "test/image-layer"
	)
	if _, err := sn.Prepare(ctx, imgKey, ""); err != nil {
		t.Fatalf("Prepare image layer: %v", err)
	}
	if err := sn.Commit(ctx, imgName, imgKey); err != nil {
		t.Fatalf("Commit image layer: %v", err)
	}

	// 2. Docker init layer (parent = normal image top layer).
	const (
		initKey  = "test/somesn-init-key"
		initName = "test/somesn-init"
	)
	if _, err := sn.Prepare(ctx, initKey, imgName); err != nil {
		t.Fatalf("Prepare docker init layer: %v", err)
	}
	if err := sn.Commit(ctx, initName, initKey); err != nil {
		t.Fatalf("Commit docker init layer: %v", err)
	}

	// 3. Docker container layer (parent = init layer).
	const containerKey = "test/somesn-container-key"
	if _, err := sn.Prepare(ctx, containerKey, initName); err != nil {
		t.Fatalf("Prepare docker container layer: %v", err)
	}

	// 4. Mounts must return a non-nil overlay mount for the normal-image case.
	mounts, err := sn.Mounts(ctx, containerKey)
	if err != nil {
		t.Fatalf("Mounts: %v", err)
	}
	if len(mounts) == 0 {
		t.Fatalf("expected non-nil overlay mount, got empty slice")
	}
	m := mounts[0]
	if m.Type != "overlay" {
		t.Errorf("expected mount type=overlay, got %q (source=%q options=%v)",
			m.Type, m.Source, m.Options)
	}

	var hasUpper, hasWork, hasLower bool
	for _, opt := range m.Options {
		switch {
		case strings.HasPrefix(opt, "upperdir="):
			hasUpper = true
		case strings.HasPrefix(opt, "workdir="):
			hasWork = true
		case strings.HasPrefix(opt, "lowerdir="):
			hasLower = true
		}
	}
	if !hasUpper || !hasWork || !hasLower {
		t.Errorf("overlay mount missing one of upper/work/lower dirs, options=%v", m.Options)
	}
}
