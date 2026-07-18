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

package builder

import (
	"testing"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestIsAttestationManifest(t *testing.T) {
	tests := []struct {
		name string
		desc v1.Descriptor
		want bool
	}{
		{
			name: "attestation annotation",
			desc: v1.Descriptor{
				Annotations: map[string]string{
					"vnd.docker.reference.type": "attestation-manifest",
				},
			},
			want: true,
		},
		{
			name: "attestation annotation with unknown platform",
			desc: v1.Descriptor{
				Annotations: map[string]string{
					"vnd.docker.reference.type": "attestation-manifest",
				},
				Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"},
			},
			want: true,
		},
		{
			name: "unknown platform without attestation annotation is not an attestation",
			desc: v1.Descriptor{
				Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"},
			},
			want: false,
		},
		{
			name: "normal image manifest",
			desc: v1.Descriptor{
				Platform: &v1.Platform{OS: "linux", Architecture: "amd64"},
			},
			want: false,
		},
		{
			name: "descriptor without attestation metadata",
			desc: v1.Descriptor{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAttestationManifest(tt.desc); got != tt.want {
				t.Errorf("isAttestationManifest() = %v, want %v", got, tt.want)
			}
		})
	}
}
