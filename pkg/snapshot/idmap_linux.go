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

import (
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/plugins/snapshots/overlay/overlayutils"
	"github.com/containerd/continuity/fs"
)

// remapSupportProbe is overridden in tests.
var remapSupportProbe = detectRemapIDsSupport

// detectRemapIDsSupport checks kernel overlay idmap support, userns FD creation,
// and backing filesystem properties before enabling remapIDs.
func detectRemapIDsSupport(root string) (bool, error) {
	ok, err := overlayutils.SupportsIDMappedMounts()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	supportsDType, err := fs.SupportsDType(root)
	if err != nil {
		return false, err
	}
	if !supportsDType {
		return false, nil
	}

	usernsFd, err := mount.GetUsernsFD("0:65534:1", "0:65534:1")
	if err != nil {
		return false, nil
	}
	_ = usernsFd.Close()

	return true, nil
}
