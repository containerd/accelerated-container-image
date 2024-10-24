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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
)

// REPOSITORY
type RepoStore struct {
	path             string
	inmemoryRepoOnly bool
	fileStore        *oci.Store
	inmemoryRepo     *inmemoryRepo
	opts             *RegistryOptions
}

type inmemoryRepo struct {
	blobs map[string][]byte
	tags  map[string]digest.Digest
}

// NewRepoStore creates a new repo store. Path provides the filesystem path to the OCI layout store. The
// inmemory component is initialized with an empty store. Both components work together to provide a
// unified view of the repository.
func NewRepoStore(ctx context.Context, path string, opts *RegistryOptions) *RepoStore {
	inmemoryRepo := &inmemoryRepo{
		blobs: make(map[string][]byte),
		tags:  make(map[string]digest.Digest),
	}

	return &RepoStore{
		path:         path,
		opts:         opts,
		inmemoryRepo: inmemoryRepo,
	}
}

// LoadStore loads the OCI layout store from the provided path
func (r *RepoStore) LoadStore(ctx context.Context) error {
	// File Store is already initialized
	if r.fileStore != nil {
		return nil
	}

	if r.opts.InmemoryRegistryOnly && !r.inmemoryRepoOnly {
		return errors.New("LoadStore should not be invoked if registry or repo is memory only")
	}

	if r.path == "" {
		return errors.New("LoadStore was not provided a path")
	}

	// Load an OCI layout store
	store, err := oci.New(r.path)
	if err != nil {
		return err
	}

	r.fileStore = store
	return nil
}

// Resolve resolves a tag to a descriptor
func (r *RepoStore) Resolve(ctx context.Context, tag string) (v1.Descriptor, error) {
	if digest, ok := r.inmemoryRepo.tags[tag]; ok {
		if blob, ok := r.inmemoryRepo.blobs[digest.String()]; ok {
			parsedManifest := v1.Manifest{}
			json.Unmarshal(blob, &parsedManifest)
			return v1.Descriptor{
				Digest:    digest,
				Size:      int64(len(blob)),
				MediaType: parsedManifest.MediaType,
			}, nil
		}
	}

	if !r.opts.InmemoryRegistryOnly && !r.inmemoryRepoOnly {
		if err := r.LoadStore(ctx); err != nil {
			return v1.Descriptor{}, err
		}

		return r.fileStore.Resolve(ctx, tag)
	}
	return v1.Descriptor{}, errdefs.ErrNotFound
}

// Fetch fetches a blob from the repository
func (r *RepoStore) Fetch(ctx context.Context, descriptor v1.Descriptor) (io.ReadCloser, error) {
	if blob, ok := r.inmemoryRepo.blobs[descriptor.Digest.String()]; ok {
		return io.NopCloser(bytes.NewReader(blob)), nil
	}

	if !r.opts.InmemoryRegistryOnly && !r.inmemoryRepoOnly {
		if err := r.LoadStore(ctx); err != nil {
			return nil, err
		}

		content, err := r.fileStore.Fetch(ctx, descriptor)

		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				return nil, errdefs.ErrNotFound // Convert to containerd error
			}
			return nil, err
		}
		return content, nil
	}
	return nil, errdefs.ErrNotFound
}

// Exists checks if a blob exists in the repository
func (r *RepoStore) Exists(ctx context.Context, descriptor v1.Descriptor) (bool, error) {
	if _, ok := r.inmemoryRepo.blobs[descriptor.Digest.String()]; ok {
		return true, nil
	}

	if !r.opts.InmemoryRegistryOnly && !r.inmemoryRepoOnly {
		if err := r.LoadStore(ctx); err != nil {
			return false, err
		}

		exists, err := r.fileStore.Exists(ctx, descriptor)

		if err != nil {
			return false, err
		}

		if exists {
			return true, nil
		}
	}

	return false, nil
}

// Push pushes a blob to the in memory repository. If the blob already exists, it returns an error.
// Tag is optional and can be empty.
func (r *RepoStore) Push(ctx context.Context, desc v1.Descriptor, tag string, content []byte) error {
	manifestExists, err := r.Exists(ctx, desc)
	if err != nil {
		return err
	}

	// Verify content by computing the digest. Real registries are expected to do this.
	contentDigest := digest.FromBytes(content)
	if contentDigest != desc.Digest {
		return fmt.Errorf("content digest %s does not match descriptor digest %s", contentDigest.String(), desc.Digest.String())
	}

	isManifest := false
	switch desc.MediaType {
	case v1.MediaTypeImageManifest, images.MediaTypeDockerSchema2Manifest:
		isManifest = true
		if r.opts.ManifestPushIgnoresLayers || manifestExists {
			break // No layer verification necessary
		}
		manifest := v1.Manifest{}
		json.Unmarshal(content, &manifest)
		for _, layer := range manifest.Layers {
			exists, err := r.Exists(ctx, layer)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("layer %s not found", layer.Digest.String())
			}
		}
	case v1.MediaTypeImageIndex, images.MediaTypeDockerSchema2ManifestList:
		isManifest = true
		if r.opts.ManifestPushIgnoresLayers || manifestExists {
			break // No manifest verification necessary
		}
		manifestList := v1.Index{}
		json.Unmarshal(content, &manifestList)
		for _, subManifestDesc := range manifestList.Manifests {
			exists, err := r.Exists(ctx, subManifestDesc)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("sub manifest %s not found", subManifestDesc.Digest.String())
			}
		}
	}
	r.inmemoryRepo.blobs[desc.Digest.String()] = content

	// We need to always store the manifest digest to tag mapping as the latest pushed manifest
	// may have a different digest than the previous one.
	if isManifest && tag != "" {
		r.inmemoryRepo.tags[tag] = desc.Digest
	}

	return nil
}
