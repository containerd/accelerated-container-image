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

	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/testsuite"
	"github.com/containerd/containerd/v2/pkg/testutil"
)

func newSnapshotterWithOpts(opts ...Opt) testsuite.SnapshotterFunc {
	return func(ctx context.Context, root string) (snapshots.Snapshotter, func() error, error) {
		cfg := DefaultBootConfig()
		cfg.Root = root
		snapshotter, err := NewSnapshotter(cfg, opts...)
		if err != nil {
			return nil, nil, err
		}

		return snapshotter, func() error { return snapshotter.Close() }, nil
	}
}

func TestBasicSnapshotterOnOverlayFS(t *testing.T) {
	testutil.RequiresRoot(t)
	testsuite.SnapshotterSuite(t, "overlaybd-on-overlayFS", newSnapshotterWithOpts())
}

func TestRootIDFromMapping(t *testing.T) {
	tests := []struct {
		name    string
		mapping string
		want    int
		wantErr bool
	}{
		{
			name:    "simple mapping",
			mapping: "0:1000:65536",
			want:    1000,
		},
		{
			name:    "root not at start of range",
			mapping: "10:1000:100",
			wantErr: true,
		},
		{
			name:    "multiple ranges, root in second",
			mapping: "1:100000:65536,0:1000:1",
			want:    1000,
		},
		{
			name:    "identity mapping",
			mapping: "0:0:65536",
			want:    0,
		},
		{
			name:    "empty mapping",
			mapping: "",
			wantErr: true,
		},
		{
			name:    "invalid format",
			mapping: "bad",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rootIDFromMapping(tt.mapping)
			if tt.wantErr {
				if err == nil {
					t.Errorf("rootIDFromMapping(%q) expected error, got %d", tt.mapping, got)
				}
				return
			}
			if err != nil {
				t.Errorf("rootIDFromMapping(%q) unexpected error: %v", tt.mapping, err)
				return
			}
			if got != tt.want {
				t.Errorf("rootIDFromMapping(%q) = %d, want %d", tt.mapping, got, tt.want)
			}
		})
	}
}

func TestAppendIDMapMountOptions(t *testing.T) {
	tests := []struct {
		name     string
		remapIDs bool
		labels   map[string]string
		wantLen  int
	}{
		{
			name:     "disabled",
			remapIDs: false,
			labels:   map[string]string{labelSnapshotUIDMapping: "0:1000:65536", labelSnapshotGIDMapping: "0:1000:65536"},
			wantLen:  0,
		},
		{
			name:     "enabled with both labels",
			remapIDs: true,
			labels:   map[string]string{labelSnapshotUIDMapping: "0:1000:65536", labelSnapshotGIDMapping: "0:1000:65536"},
			wantLen:  2,
		},
		{
			name:     "enabled with uid only",
			remapIDs: true,
			labels:   map[string]string{labelSnapshotUIDMapping: "0:1000:65536"},
			wantLen:  1,
		},
		{
			name:     "enabled with no labels",
			remapIDs: true,
			labels:   map[string]string{},
			wantLen:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &snapshotter{remapIDs: tt.remapIDs}
			info := snapshots.Info{Labels: tt.labels}
			result := o.appendIDMapMountOptions(nil, info)
			if len(result) != tt.wantLen {
				t.Errorf("appendIDMapMountOptions() returned %d options, want %d: %v", len(result), tt.wantLen, result)
			}
		})
	}
}

func TestEnsureIDMappedLower_skipsWithoutRemapIDs(t *testing.T) {
	o := &snapshotter{
		root:     t.TempDir(),
		remapIDs: false,
	}
	info := snapshots.Info{Labels: map[string]string{
		labelSnapshotUIDMapping: "0:100000:65536",
		labelSnapshotGIDMapping: "0:100000:65536",
	}}
	got, err := o.ensureIDMappedLower(context.Background(), "1", "8", info)
	if err != nil {
		t.Fatalf("ensureIDMappedLower() error: %v", err)
	}
	if got != o.overlaybdMountpoint("8") {
		t.Fatalf("ensureIDMappedLower() = %q, want %q", got, o.overlaybdMountpoint("8"))
	}
}

func TestOverlayOptions_omitIdmapWhenLowerPreMapped(t *testing.T) {
	o := &snapshotter{
		root:     t.TempDir(),
		remapIDs: true,
		indexOff: true,
	}
	info := snapshots.Info{Labels: map[string]string{
		labelSnapshotUIDMapping: "0:100000:65536",
		labelSnapshotGIDMapping: "0:100000:65536",
	}}

	lowerPath := o.idmappedLowerPath("126")
	mountpoint := o.overlaybdMountpoint("8")

	var options []string
	options = append(options, "index=off", "lowerdir="+lowerPath)
	if lowerPath == mountpoint {
		options = o.appendIDMapMountOptions(options, info)
	}

	if hasMountOptionPrefix(options, "uidmap=") || hasMountOptionPrefix(options, "gidmap=") {
		t.Fatalf("pre-idmapped lower must not include uidmap/gidmap, got: %v", options)
	}
}

func hasMountOptionPrefix(options []string, prefix string) bool {
	for _, o := range options {
		if strings.HasPrefix(o, prefix) {
			return true
		}
	}
	return false
}
