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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/log"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestNewSnapshotterMaxDataAreaMB(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		want    int
		wantErr bool
	}{
		{name: "omitted", config: `{}`, want: 4},
		{name: "zero", config: `{"maxDataAreaMB":0}`, want: 4},
		{name: "null", config: `{"maxDataAreaMB":null}`, want: 4},
		{name: "explicit default", config: `{"maxDataAreaMB":4}`, want: 4},
		{name: "16 MiB", config: `{"maxDataAreaMB":16}`, want: 16},
		{name: "64 MiB", config: `{"maxDataAreaMB":64}`, want: 64},
		{name: "128 MiB", config: `{"maxDataAreaMB":128}`, want: 128},
		{name: "256 MiB", config: `{"maxDataAreaMB":256}`, want: 256},
		{name: "negative", config: `{"maxDataAreaMB":-1}`, wantErr: true},
		{name: "kernel integer overflow", config: `{"maxDataAreaMB":2147483648}`, wantErr: true},
		{name: "fractional", config: `{"maxDataAreaMB":4.5}`, wantErr: true},
		{name: "string", config: `{"maxDataAreaMB":"64"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Decode defaults plus JSON as the service does, with no existing store.
			cfg := DefaultBootConfig()
			cfg.Root = filepath.Join(t.TempDir(), "snapshotter")
			err := json.Unmarshal([]byte(tt.config), cfg)
			if err != nil {
				if !tt.wantErr {
					t.Fatal(err)
				}
				return
			}

			sn, err := NewSnapshotter(cfg)
			if err == nil {
				t.Cleanup(func() {
					if err := sn.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			if tt.wantErr {
				// Invalid configuration must fail before creating metadata or directories.
				if err == nil {
					t.Fatal("NewSnapshotter accepted an invalid maxDataAreaMB")
				}
				if _, err := os.Stat(cfg.Root); !os.IsNotExist(err) {
					t.Fatalf("invalid configuration created a snapshotter root: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			// Each instance captures its setting independently of later config changes.
			cfg.MaxDataAreaMB = 1
			if got := sn.(*snapshotter).maxDataAreaMB; got != tt.want {
				t.Fatalf("snapshotter maxDataAreaMB = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewSnapshotterMaxDataAreaMBZeroValue(t *testing.T) {
	// Existing Go callers can construct BootConfig without the new field.
	cfg := &BootConfig{Root: t.TempDir()}
	sn, err := NewSnapshotter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sn.Close(); err != nil {
			t.Error(err)
		}
	})
	if got := sn.(*snapshotter).maxDataAreaMB; got != 4 {
		t.Fatalf("zero-value maxDataAreaMB = %d, want 4", got)
	}
}

func TestAttachDeviceRejectsInvalidMaxDataAreaMB(t *testing.T) {
	// Reject invalid direct callers before touching the host's configfs hierarchy.
	params := NewAttachDeviceParams("invalid-data-area", -1, "config.json", "init-debug.log", false)
	params.MaxDataAreaMB = -1
	dev, err := AttachDevice(context.Background(), params)
	if err == nil || !strings.Contains(err.Error(), "maxDataAreaMB") || dev != "" {
		t.Fatalf("AttachDevice() = (%q, %v), want no device and a configuration error", dev, err)
	}
}

func TestSetMaxDataAreaMB(t *testing.T) {
	tests := []struct {
		name          string
		requested     int
		readback      string
		missing       bool
		readbackIsDir bool
		controlIsDir  bool
		wantWarning   bool
		wantErr       string
	}{
		{name: "default", requested: 4, readback: "4\n"},
		{name: "64 MiB", requested: 64, readback: "64\n"},
		{name: "128 MiB", requested: 128, readback: "128\n"},
		{name: "clamped", requested: 64, readback: "16\n", wantErr: "requested 64 MiB, effective 16 MiB"},
		{name: "larger than requested", requested: 64, readback: "128\n", wantErr: "requested 64 MiB, effective 128 MiB"},
		{name: "zero readback", requested: 64, readback: "0\n", wantErr: "requested 64 MiB, effective 0 MiB"},
		{name: "malformed readback", requested: 64, readback: "invalid\n", wantErr: "invalid TCMU data area"},
		{name: "legacy kernel default", requested: 4, missing: true, wantWarning: true},
		{name: "legacy kernel custom size", requested: 64, missing: true, wantWarning: true},
		{name: "unreadable readback", requested: 64, readbackIsDir: true, wantErr: "failed to read TCMU data area"},
		{name: "control write failure", requested: 64, readback: "64\n", controlIsDir: true, wantErr: "failed to set TCMU data area"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use real files with a preset kernel response to model success and clamping.
			target := t.TempDir()
			attrib := filepath.Join(target, "attrib")
			if err := os.Mkdir(attrib, 0700); err != nil {
				t.Fatal(err)
			}
			if tt.readbackIsDir {
				if err := os.Mkdir(filepath.Join(attrib, "max_data_area_mb"), 0700); err != nil {
					t.Fatal(err)
				}
			} else if !tt.missing {
				if err := os.WriteFile(filepath.Join(attrib, "max_data_area_mb"), []byte(tt.readback), 0600); err != nil {
					t.Fatal(err)
				}
			}
			control := filepath.Join(target, "control")
			if tt.controlIsDir {
				if err := os.Mkdir(control, 0700); err != nil {
					t.Fatal(err)
				}
			}

			// Capture logs per device so the legacy fallback remains observable.
			logger, hook := logtest.NewNullLogger()
			ctx := log.WithLogger(context.Background(), logrus.NewEntry(logger))
			err := setMaxDataAreaMB(ctx, target, tt.requested)
			entries := hook.AllEntries()
			if tt.wantWarning {
				// Missing capability preserves attachment and reports the unverified size.
				if len(entries) != 1 || entries[0].Level != logrus.WarnLevel {
					t.Fatalf("logs = %v, want one warning", entries)
				}
				entry := entries[0]
				for _, text := range []string{target, fmt.Sprintf("requested %d MiB", tt.requested), "kernel may ignore", "continuing without size verification"} {
					if !strings.Contains(entry.Message, text) {
						t.Fatalf("warning = %q, want %q", entry.Message, text)
					}
				}
				cause, ok := entry.Data[logrus.ErrorKey].(error)
				if !ok || !os.IsNotExist(cause) {
					t.Fatalf("warning error = %v, want the missing-attribute error", entry.Data[logrus.ErrorKey])
				}
			} else {
				for _, entry := range entries {
					if entry.Level == logrus.WarnLevel {
						t.Fatalf("unexpected warning: %s", entry.Message)
					}
				}
			}
			if tt.wantErr != "" {
				// A successful control write alone must not hide a rejected size.
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("setMaxDataAreaMB() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(control)
			if err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("max_data_area_mb=%d", tt.requested); string(data) != want {
				t.Fatalf("control = %q, want %q", data, want)
			}
		})
	}
}
