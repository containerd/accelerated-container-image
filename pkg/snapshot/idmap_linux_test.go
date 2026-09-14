//go:build linux

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

import "testing"

func TestNewSnapshotter_remapIDsRequiresSupportProbe(t *testing.T) {
	orig := remapSupportProbe
	t.Cleanup(func() { remapSupportProbe = orig })

	root := t.TempDir()
	cfg := DefaultBootConfig()
	cfg.Root = root
	cfg.RemapIDs = true

	remapSupportProbe = func(string) (bool, error) {
		return false, nil
	}
	sn, err := NewSnapshotter(cfg)
	if err != nil {
		t.Fatalf("NewSnapshotter() error: %v", err)
	}
	defer sn.Close()

	if sn.(*snapshotter).remapIDs {
		t.Fatal("remapIDs should stay disabled when support probe fails")
	}

	remapSupportProbe = func(string) (bool, error) {
		return true, nil
	}
	sn2, err := NewSnapshotter(cfg)
	if err != nil {
		t.Fatalf("NewSnapshotter() error: %v", err)
	}
	defer sn2.Close()

	if !sn2.(*snapshotter).remapIDs {
		t.Fatal("remapIDs should be enabled when support probe succeeds")
	}
}
