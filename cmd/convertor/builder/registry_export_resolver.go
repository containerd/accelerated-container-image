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
	"context"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/log"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// RegistryExportResolver implements remotes.Resolver that:
// - Fetches from import content store (tar) 
// - Pushes to registry using a registry resolver
type RegistryExportResolver struct {
	store            content.Store
	imageStore       images.Store  
	registryResolver remotes.Resolver // For creating registry pushers
}

// NewRegistryExportResolver creates a resolver for tar import -> registry export
func NewRegistryExportResolver(importStore content.Store, importImageStore images.Store, registryResolver remotes.Resolver) *RegistryExportResolver {
	return &RegistryExportResolver{
		store:            importStore,
		imageStore:       importImageStore,
		registryResolver: registryResolver,
	}
}

// Resolve resolves a reference from the import store
func (r *RegistryExportResolver) Resolve(ctx context.Context, ref string) (string, v1.Descriptor, error) {
	log.G(ctx).Debugf("registry export resolver: resolving reference: %s", ref)
	
	// Look up in import image store
	image, err := r.imageStore.Get(ctx, ref)
	if err != nil {
		return "", v1.Descriptor{}, err
	}
	
	return ref, image.Target, nil
}

// Fetcher returns a fetcher for the import content store
func (r *RegistryExportResolver) Fetcher(ctx context.Context, ref string) (remotes.Fetcher, error) {
	return &ContentStoreFetcher{
		store: r.store,
	}, nil
}

// Pusher returns a registry pusher from the registry resolver
func (r *RegistryExportResolver) Pusher(ctx context.Context, ref string) (remotes.Pusher, error) {
	log.G(ctx).Debugf("registry export resolver: creating registry pusher for ref: %s", ref)
	return r.registryResolver.Pusher(ctx, ref)
}