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

package testingresources

import (
	"encoding/json"
	"errors"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// Docker images seem to be created with descriptors whose order
// matches mediaType -> size -> digest, oci images seem to be created with descriptors
// whose order matches mediaType -> digest -> size. Json marshalling in golang will
// result in the previous version of the descriptor given the order of the fields defined
// in the struct. This is why we need to provide a custom descriptor marshalling function
// for docker images.

type DockerDescriptor struct {
	// MediaType is the media type of the object this schema refers to.
	MediaType string `json:"mediaType,omitempty"`

	// Size specifies the size in bytes of the blob.
	Size int64 `json:"size"`

	// Digest is the digest of the targeted content.
	Digest digest.Digest `json:"digest"`

	// URLs specifies a list of URLs from which this object MAY be downloaded
	URLs []string `json:"urls,omitempty"`

	// Annotations contains arbitrary metadata relating to the targeted content.
	Annotations map[string]string `json:"annotations,omitempty"`

	// Data is an embedding of the targeted content. This is encoded as a base64
	// string when marshalled to JSON (automatically, by encoding/json). If
	// present, Data can be used directly to avoid fetching the targeted content.
	Data []byte `json:"data,omitempty"`

	// Platform describes the platform which the image in the manifest runs on.
	//
	// This should only be used when referring to a manifest.
	Platform *v1.Platform `json:"platform,omitempty"`

	// ArtifactType is the IANA media type of this artifact.
	ArtifactType string `json:"artifactType,omitempty"`
}

type DockerManifest struct {
	specs.Versioned

	// MediaType specifies the type of this document data structure e.g. `application/vnd.oci.image.manifest.v1+json`
	MediaType string `json:"mediaType,omitempty"`

	// Config references a configuration object for a container, by digest.
	// The referenced configuration object is a JSON blob that the runtime uses to set up the container.
	Config DockerDescriptor `json:"config"`

	// Layers is an indexed list of layers referenced by the manifest.
	Layers []DockerDescriptor `json:"layers"`

	// Subject is an optional link from the image manifest to another manifest forming an association between the image manifest and the other manifest.
	Subject *DockerDescriptor `json:"subject,omitempty"`

	// Annotations contains arbitrary metadata for the image manifest.
	Annotations map[string]string `json:"annotations,omitempty"`
}

func ConsistentManifestMarshal(manifest *v1.Manifest) ([]byte, error) {
	// If OCI Manifest
	if manifest.MediaType == v1.MediaTypeImageManifest {
		return json.MarshalIndent(manifest, "", "   ")
	}

	if manifest.MediaType != images.MediaTypeDockerSchema2Manifest {
		return nil, errors.New("unsupported manifest media type")
	}

	// If Docker Manifest
	content, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}

	// Remarshal from dockerManifest Struct
	var dockerManifest DockerManifest
	err = json.Unmarshal(content, &dockerManifest)
	if err != nil {
		return nil, err
	}

	return json.MarshalIndent(dockerManifest, "", "   ")
}
