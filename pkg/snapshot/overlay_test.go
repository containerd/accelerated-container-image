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

func TestDefaultBootConfigExperimental(t *testing.T) {
	cfg := DefaultBootConfig()

	if cfg.Experimental.Enabled {
		t.Fatal("expected experimental to be disabled by default")
	}
	if !cfg.Experimental.PreAuth {
		t.Fatal("expected preAuth to be enabled by default")
	}
	if cfg.Experimental.OverlaybdApiServer != "http://127.0.0.1:9862" {
		t.Fatalf("unexpected overlaybd api server address %q", cfg.Experimental.OverlaybdApiServer)
	}
}

func TestPreAuthEnabled(t *testing.T) {
	tests := []struct {
		name         string
		experimental ExperimentalConfig
		want         bool
	}{
		{
			name: "experimental disabled",
			experimental: ExperimentalConfig{
				Enabled: false,
				PreAuth: true,
			},
			want: false,
		},
		{
			name: "preAuth disabled",
			experimental: ExperimentalConfig{
				Enabled: true,
				PreAuth: false,
			},
			want: false,
		},
		{
			name: "experimental and preAuth enabled",
			experimental: ExperimentalConfig{
				Enabled: true,
				PreAuth: true,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sn := &snapshotter{experimental: tt.experimental}
			if got := sn.preAuthEnabled(); got != tt.want {
				t.Fatalf("preAuthEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBasicSnapshotterOnOverlayFS(t *testing.T) {
	testutil.RequiresRoot(t)
	testsuite.SnapshotterSuite(t, "overlaybd-on-overlayFS", newSnapshotterWithOpts())
}
