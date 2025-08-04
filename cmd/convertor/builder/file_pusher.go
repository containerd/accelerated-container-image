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
	"fmt"
	"os"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// FileBasedResolver implements remotes.Resolver that captures pushed content locally
// for later export to tar files
type FileBasedResolver struct {
	store       content.Store
	imageStore  images.Store
	outputStore content.Store  // Where converted layers are stored
	outputImageStore images.Store
	tempDir     string        // Path to temporary directory for cleanup
}

// NewFileBasedResolver creates a resolver that captures converted content locally
func NewFileBasedResolver(importStore content.Store, importImageStore images.Store) (*FileBasedResolver, error) {
	// Create temporary directory for output content store
	tempDir, err := os.MkdirTemp("", "convertor-output-*")
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create temp directory for output store")
	}

	// Create local content store for converted layers
	outputStore, err := local.NewStore(tempDir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create output content store")
	}

	// Create in-memory image store for converted images
	outputImageStore := &memoryImageStore{
		images: make(map[string]images.Image),
	}

	return &FileBasedResolver{
		store:            importStore,
		imageStore:       importImageStore,
		outputStore:      outputStore,
		outputImageStore: outputImageStore,
		tempDir:          tempDir,
	}, nil
}

// Store returns the import content store (for reading original layers)
func (r *FileBasedResolver) Store() content.Store {
	return r.store
}

// ImageStore returns the import image store
func (r *FileBasedResolver) ImageStore() images.Store {
	return r.imageStore
}

// OutputStore returns the output content store (containing converted layers)
func (r *FileBasedResolver) OutputStore() content.Store {
	return r.outputStore
}

// OutputImageStore returns the output image store (containing converted manifests)
func (r *FileBasedResolver) OutputImageStore() images.Store {
	return r.outputImageStore
}

// Resolve resolves a reference from the import store
func (r *FileBasedResolver) Resolve(ctx context.Context, ref string) (string, v1.Descriptor, error) {
	log.G(ctx).Debugf("file-based resolver: resolving reference: %s", ref)
	
	// Look up in import image store
	image, err := r.imageStore.Get(ctx, ref)
	if err != nil {
		return "", v1.Descriptor{}, errors.Wrapf(errdefs.ErrNotFound, "image not found: %s", ref)
	}

	return ref, image.Target, nil
}

// Fetcher returns a fetcher for the import content store
func (r *FileBasedResolver) Fetcher(ctx context.Context, ref string) (remotes.Fetcher, error) {
	return &ContentStoreFetcher{
		store: r.store,
	}, nil
}

// Pusher returns a file-based pusher that captures converted content locally
func (r *FileBasedResolver) Pusher(ctx context.Context, ref string) (remotes.Pusher, error) {
	log.G(ctx).Debugf("file-based resolver: creating pusher for ref: %s", ref)
	
	return &FilePusher{
		ref:        ref,
		store:      r.outputStore,
		imageStore: r.outputImageStore,
	}, nil
}

// FilePusher implements remotes.Pusher by writing content to a local content store
type FilePusher struct {
	ref        string
	store      content.Store
	imageStore images.Store
}

// Push writes content to the local output content store
func (p *FilePusher) Push(ctx context.Context, desc v1.Descriptor) (content.Writer, error) {
	log.G(ctx).Debugf("file pusher: pushing blob: %s (size: %d)", desc.Digest, desc.Size)

	// Check if content already exists
	_, err := p.store.Info(ctx, desc.Digest)
	if err == nil {
		log.G(ctx).Debugf("file pusher: content already exists: %s", desc.Digest)
		return &nopWriter{desc: desc}, nil
	}

	// Create writer for the content
	writer, err := p.store.Writer(ctx, content.WithRef(remotes.MakeRefKey(ctx, desc)), content.WithDescriptor(desc))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create writer for %s", desc.Digest)
	}

	return &fileWriter{
		Writer: writer,
		desc:   desc,
		pusher: p,
	}, nil
}

// Writer returns a content writer (not used by overlaybd conversion)
func (p *FilePusher) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	return nil, errors.New("Writer not implemented")
}

// fileWriter wraps content.Writer and handles manifest storage
type fileWriter struct {
	content.Writer
	desc   v1.Descriptor
	pusher *FilePusher
}

func (w *fileWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	log.G(ctx).Debugf("file pusher: committing blob: %s", w.desc.Digest)
	
	err := w.Writer.Commit(ctx, size, expected, opts...)
	if err != nil {
		return err
	}

	// If this is a manifest, store it in the image store as well
	if isManifestMediaType(w.desc.MediaType) {
		log.G(ctx).Debugf("file pusher: storing manifest in image store: %s", w.desc.Digest)
		
		// Generate image name based on the pusher's reference
		imageName := fmt.Sprintf("converted:%s", w.desc.Digest.Encoded()[:12])
		if w.pusher.ref != "" {
			// Extract name from reference (remove @digest part if present)
			if idx := len(w.pusher.ref); idx > 0 {
				imageName = w.pusher.ref
				if atIdx := len(imageName); atIdx > 0 && imageName[atIdx-1:] == "@" {
					imageName = imageName[:atIdx-1]
				}
			}
		}

		image := images.Image{
			Name:      imageName,
			Target:    w.desc,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		
		_, err = w.pusher.imageStore.Create(ctx, image)
		if err != nil && !errdefs.IsAlreadyExists(err) {
			log.G(ctx).Warnf("failed to store image in image store: %v", err)
		} else {
			log.G(ctx).Debugf("file pusher: stored image: %s -> %s", imageName, w.desc.Digest)
		}
	}

	return nil
}

// isManifestMediaType checks if a media type represents a manifest
func isManifestMediaType(mediaType string) bool {
	manifestTypes := []string{
		v1.MediaTypeImageManifest,
		"application/vnd.docker.distribution.manifest.v2+json",
		v1.MediaTypeImageIndex,
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}
	
	for _, mt := range manifestTypes {
		if mediaType == mt {
			return true
		}
	}
	return false
}

// CleanupTempDir removes the temporary directory used by the output store
func (r *FileBasedResolver) CleanupTempDir() error {
	if r.tempDir != "" {
		log.L.Debugf("cleaning up temporary output directory: %s", r.tempDir)
		return os.RemoveAll(r.tempDir)
	}
	return nil
}

// GetTempDir returns the temporary directory path for debugging  
func (r *FileBasedResolver) GetTempDir() string {
	return r.tempDir
}