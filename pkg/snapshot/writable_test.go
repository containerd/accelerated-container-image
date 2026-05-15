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
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/containerd/v2/core/snapshots"
)

func TestGetWritableTypeRequiresOverlayBDParent(t *testing.T) {
	// createMountPoint calls getWritableType with the parent snapshot. The
	// dir/dev paths must only be selected when that parent is an overlaybd
	// layer: a parentless prepare can also be an image's first layer or the
	// rootfs of a zero-layer image, and those must stay read-only.
	writeParentConfig := func(t *testing.T, o *snapshotter, id string) {
		t.Helper()
		path := o.overlaybdConfPath(id)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("failed to create parent block dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(`{"lowers":[]}`), 0644); err != nil {
			t.Fatalf("failed to write parent config: %v", err)
		}
	}

	tests := []struct {
		name        string
		rwMode      string
		parent      string
		parentIsOBD bool
		labels      map[string]string
		want        string
	}{
		{
			name:   "parentless prepare stays read-only",
			rwMode: RwDir,
			want:   RoDir,
		},
		{
			name:   "parentless prepare honors explicit writable label",
			rwMode: RoDir,
			labels: map[string]string{label.SupportReadWriteMode: RwDev},
			want:   RwDev,
		},
		{
			name:   "parent is not overlaybd",
			rwMode: RwDev,
			parent: "normal-layer",
			want:   RoDir,
		},
		{
			name:        "parent is overlaybd follows rwMode",
			rwMode:      RwDev,
			parent:      "overlaybd-layer",
			parentIsOBD: true,
			want:        RwDev,
		},
		{
			name:        "explicit read-only label wins over rwMode",
			rwMode:      RwDir,
			parent:      "overlaybd-layer",
			parentIsOBD: true,
			labels:      map[string]string{label.SupportReadWriteMode: RoDir},
			want:        RoDir,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &snapshotter{root: t.TempDir(), rwMode: tt.rwMode}
			if tt.parentIsOBD {
				writeParentConfig(t, o, tt.parent)
			}
			got := o.getWritableType(context.Background(), tt.parent, snapshots.Info{Labels: tt.labels})
			if got != tt.want {
				t.Fatalf("getWritableType() = %q, want %q", got, tt.want)
			}
		})
	}
}
