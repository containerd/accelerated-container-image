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

package convertor

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestIsProvenanceLayer(t *testing.T) {
	tests := []struct {
		name     string
		desc     ocispec.Descriptor
		expected bool
	}{
		{
			name: "in-toto attestation layer",
			desc: ocispec.Descriptor{
				MediaType: "application/vnd.in-toto+json",
				Digest:    "sha256:abc123",
				Size:      1024,
			},
			expected: true,
		},
		{
			name: "cosign signature layer",
			desc: ocispec.Descriptor{
				MediaType: "application/vnd.dev.cosign.simplesigning.v1+json",
				Digest:    "sha256:def456",
				Size:      512,
			},
			expected: true,
		},
		{
			name: "sigstore bundle layer",
			desc: ocispec.Descriptor{
				MediaType: "application/vnd.dev.sigstore.bundle+json",
				Digest:    "sha256:ghi789",
				Size:      256,
			},
			expected: true,
		},
		{
			name: "layer with in-toto annotation",
			desc: ocispec.Descriptor{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    "sha256:stu901",
				Size:      1024,
				Annotations: map[string]string{
					"in-toto.io/predicate-type": "https://slsa.dev/provenance/v0.2",
				},
			},
			expected: true,
		},
		{
			name: "layer with docker attestation reference",
			desc: ocispec.Descriptor{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    "sha256:vwx234",
				Size:      2048,
				Annotations: map[string]string{
					"vnd.docker.reference.type": "attestation-manifest",
				},
			},
			expected: true,
		},
		{
			name: "layer with provenance artifact type",
			desc: ocispec.Descriptor{
				MediaType:    "application/vnd.docker.distribution.manifest.v2+json",
				ArtifactType: "application/vnd.example.provenance+json",
				Digest:       "sha256:jkl012",
				Size:         2048,
			},
			expected: true,
		},
		{
			name: "regular container layer",
			desc: ocispec.Descriptor{
				MediaType: "application/vnd.docker.image.rootfs.diff.tar.gzip",
				Digest:    "sha256:regular123",
				Size:      5242880,
			},
			expected: false,
		},
		{
			name: "oci layer",
			desc: ocispec.Descriptor{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    "sha256:oci456",
				Size:      10485760,
			},
			expected: false,
		},
		{
			name: "image config",
			desc: ocispec.Descriptor{
				MediaType: "application/vnd.oci.image.config.v1+json",
				Digest:    "sha256:config789",
				Size:      1024,
			},
			expected: false,
		},
		{
			name: "empty descriptor",
			desc: ocispec.Descriptor{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isProvenanceLayer(tt.desc)
			if result != tt.expected {
				t.Errorf("isProvenanceLayer() = %v, expected %v for %s", result, tt.expected, tt.name)
			}
		})
	}
}

// Test integration showing layer filtering behavior
func TestConvertLayersSkipProvenance(t *testing.T) {
	// Create mock layer descriptors
	layers := []ocispec.Descriptor{
		// Regular filesystem layer
		{
			MediaType: "application/vnd.docker.image.rootfs.diff.tar.gzip",
			Digest:    "sha256:layer1",
			Size:      5242880,
		},
		// Provenance layer (should be skipped)
		{
			MediaType: "application/vnd.in-toto+json",
			Digest:    "sha256:provenance1",
			Size:      2048,
			Annotations: map[string]string{
				"in-toto.io/predicate-type": "https://slsa.dev/provenance/v0.2",
			},
		},
		// Another regular filesystem layer
		{
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Digest:    "sha256:layer2",
			Size:      10485760,
		},
		// Cosign signature layer (should be skipped)
		{
			MediaType: "application/vnd.dev.cosign.simplesigning.v1+json",
			Digest:    "sha256:signature1",
			Size:      512,
		},
	}

	// Count layers that would be processed vs skipped
	filesystemLayers := 0
	provenanceLayers := 0

	for _, layer := range layers {
		if isProvenanceLayer(layer) {
			provenanceLayers++
		} else {
			filesystemLayers++
		}
	}

	// Verify expected counts
	expectedFilesystemLayers := 2
	expectedProvenanceLayers := 2

	if filesystemLayers != expectedFilesystemLayers {
		t.Errorf("Expected %d filesystem layers, got %d", expectedFilesystemLayers, filesystemLayers)
	}

	if provenanceLayers != expectedProvenanceLayers {
		t.Errorf("Expected %d provenance layers, got %d", expectedProvenanceLayers, provenanceLayers)
	}

	// Verify specific layer classifications
	if isProvenanceLayer(layers[0]) {
		t.Errorf("Expected regular filesystem layer to NOT be identified as provenance layer")
	}

	if !isProvenanceLayer(layers[1]) {
		t.Errorf("Expected in-toto layer to be identified as provenance layer")
	}

	if isProvenanceLayer(layers[2]) {
		t.Errorf("Expected regular filesystem layer to NOT be identified as provenance layer")
	}

	if !isProvenanceLayer(layers[3]) {
		t.Errorf("Expected cosign layer to be identified as provenance layer")
	}
}