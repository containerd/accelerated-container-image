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

package utils

import (
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// IsProvenanceDescriptor checks if a descriptor represents provenance/attestation metadata
func IsProvenanceDescriptor(desc v1.Descriptor) bool {
	// Check for common provenance and attestation media types
	provenanceTypes := []string{
		"application/vnd.in-toto+json",
		"application/vnd.dev.cosign.simplesigning.v1+json",
		"application/vnd.dev.sigstore.bundle+json",
	}

	for _, pType := range provenanceTypes {
		if strings.Contains(desc.MediaType, pType) {
			return true
		}
	}

	// Check for provenance-related artifact types
	if desc.ArtifactType != "" {
		if strings.Contains(desc.ArtifactType, "provenance") ||
			strings.Contains(desc.ArtifactType, "attestation") ||
			strings.Contains(desc.ArtifactType, "signature") {
			return true
		}
	}

	// Check annotations for provenance markers
	if desc.Annotations != nil {
		if _, exists := desc.Annotations["in-toto.io/predicate-type"]; exists {
			return true
		}
		if _, exists := desc.Annotations["vnd.docker.reference.type"]; exists {
			if refType := desc.Annotations["vnd.docker.reference.type"]; refType == "attestation-manifest" || strings.Contains(refType, "provenance") {
				return true
			}
		}
	}

	return false
}
