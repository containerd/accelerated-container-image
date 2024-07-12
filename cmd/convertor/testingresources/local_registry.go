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
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// REGISTRY
// TestRegistry is a mock registry that can be used for testing purposes.
// The implementation is a combination of in memory and local storage, where
// the in memory storage is used for pushes and overrides. The local storage
// provides a prebuilt index of repositories and manifests for pulls.
// Features: Pull, Push, Resolve.
// Limitations: Cross Repository Mounts are not currently supported, Delete
// is not supported.
type TestRegistry struct {
	internalRegistry internalRegistry
	opts             RegistryOptions
}

type RegistryOptions struct {
	InmemoryRegistryOnly      bool   // Specifies if the registry should not load any resources from storage
	LocalRegistryPath         string // Specifies the path to the local registry
	ManifestPushIgnoresLayers bool   // Specifies if the registry should require layers to be pushed before manifest
}

type internalRegistry map[string]*RepoStore

func NewTestRegistry(ctx context.Context, opts RegistryOptions) (*TestRegistry, error) {
	TestRegistry := TestRegistry{
		internalRegistry: make(internalRegistry),
		opts:             opts,
	}
	if !opts.InmemoryRegistryOnly {
		files, err := os.ReadDir(opts.LocalRegistryPath)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if file.IsDir() {
				// Actual load from storage is done in a deferred manner as an optimization
				repoStore := NewRepoStore(
					ctx,
					filepath.Join(opts.LocalRegistryPath, file.Name()),
					&opts,
				)
				TestRegistry.internalRegistry[file.Name()] = repoStore
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return &TestRegistry, nil
}

func (r *TestRegistry) Resolve(ctx context.Context, ref string) (v1.Descriptor, error) {
	_, repository, tag, err := ParseRef(ctx, ref)
	if err != nil {
		return v1.Descriptor{}, err
	}
	if repo, ok := r.internalRegistry[repository]; ok {
		return repo.Resolve(ctx, tag)
	}
	return v1.Descriptor{}, errors.New("Repository not found")
}

func (r *TestRegistry) Fetch(ctx context.Context, repository string, descriptor v1.Descriptor) (io.ReadCloser, error) {
	// Add in memory store for overrides/pushes
	if repo, ok := r.internalRegistry[repository]; ok {
		return repo.Fetch(ctx, descriptor)
	}
	return nil, errdefs.ErrNotFound
}

// Push Adds content to the in-memory store
func (r *TestRegistry) Push(ctx context.Context, repository string, tag string, descriptor v1.Descriptor, content []byte) error {
	repo, ok := r.internalRegistry[repository]
	if ok {
		return repo.Push(ctx, descriptor, tag, content)
	}

	// If the repository does not exist we create a new one inmemory
	repo = NewRepoStore(ctx, repository, &r.opts)
	repo.inmemoryRepoOnly = true
	r.internalRegistry[repository] = repo
	repo.Push(ctx, descriptor, tag, content)

	return nil
}

func (r *TestRegistry) Exists(ctx context.Context, repository string, tag string, desc v1.Descriptor) (bool, error) {
	repo, ok := r.internalRegistry[repository]
	if !ok {
		return false, nil
	}

	// If no tag needs to be verified we only care about the digest
	if tag == "" {
		return repo.Exists(ctx, desc)
	}

	// If a tag is specified for a digest we need to match the tag to the digest
	switch desc.MediaType {
	case v1.MediaTypeImageManifest, v1.MediaTypeImageIndex,
		images.MediaTypeDockerSchema2Manifest, images.MediaTypeDockerSchema2ManifestList:
		digest, ok := repo.inmemoryRepo.tags[tag]
		if !ok {
			return false, nil
		}
		if digest != desc.Digest {
			return false, nil
		}
		// Tag matches provided digest. Since no delete option exists
		// we don't need to check if the digest exists in the repo stores.
		return true, nil
	default:
		return repo.Exists(ctx, desc)
	}
}

// Mount simulates a cross repo mount by copying blobs from srcRepository to targetRepository
func (r *TestRegistry) Mount(ctx context.Context, srcRepository string, targetRepository string, desc v1.Descriptor) error {
	rd, err := r.Fetch(ctx, srcRepository, desc)
	if err != nil {
		return err
	}
	defer rd.Close()
	var body []byte
	if body, err = io.ReadAll(rd); err != nil {
		return err
	}
	return r.Push(ctx, targetRepository, "", desc, body)
}
