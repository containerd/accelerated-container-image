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

package version

import (
	"fmt"

	"golang.org/x/mod/semver"
)

const (
	OverlayBDVersionNumber     = "0.1.0"
	TurboOCIVersionNumber      = "0.1.0-turbo.ociv1"
	DeprecatedOCIVersionNumber = "0.1.0-fastoci" // old version of turboOCI
)

const (
	UserspaceConsistencyLayerVersion    = "1" // This should be updated when the layer format changes from userspace convertor side independent of the underlying overlaybd tools
	UserspaceConsistencyManifestVersion = "1" // This should be updated when the manifest format changes from userspace convertor side independent of the underlying overlaybd tools
)

// Compound version to be used for the database version
type UserspaceVersion struct {
	LayerVersion    string
	ManifestVersion string
}

// GetUserSpaceConsistencyVersion returns the version of the userspace conversion for use with manifest and layer deduplication.
func GetUserSpaceConsistencyVersion() UserspaceVersion {
	// Only the major version should denote a breaking change on the layer format
	toolsMajorVersion := semver.Major(GetOverlaybdToolsVersion())

	return UserspaceVersion{
		LayerVersion:    fmt.Sprintf("%s-%s", UserspaceConsistencyLayerVersion, toolsMajorVersion),
		ManifestVersion: UserspaceConsistencyManifestVersion,
	}
}

// GetOVerlaybdVersion returns the version of the overlaybd tools. This value should be obtained from the tools
// themselves, and not hardcoded in this file. This is a placeholder value.
func GetOverlaybdToolsVersion() string {
	return "v0.1.0" // This is a placeholder value
}
