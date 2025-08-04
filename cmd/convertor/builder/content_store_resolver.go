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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/images/archive"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ContentStoreResolver implements remotes.Resolver using a content store
type ContentStoreResolver struct {
	store      content.Store
	imageStore images.Store
	tracker    docker.StatusTracker
}

// Store returns the underlying content store
func (r *ContentStoreResolver) Store() content.Store {
	return r.store
}

// ImageStore returns the underlying image store
func (r *ContentStoreResolver) ImageStore() images.Store {
	return r.imageStore
}

// NewContentStoreResolver creates a new resolver backed by a content store
func NewContentStoreResolver(store content.Store, imageStore images.Store) *ContentStoreResolver {
	return &ContentStoreResolver{
		store:      store,
		imageStore: imageStore,
		tracker:    docker.NewInMemoryTracker(),
	}
}

// NewContentStoreResolverFromTar creates a resolver by importing a tar file
func NewContentStoreResolverFromTar(ctx context.Context, tarPath string) (*ContentStoreResolver, error) {
	// Create temporary directory for content store
	tempDir, err := os.MkdirTemp("", "convertor-import-*")
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create temp directory")
	}

	// Create local content store
	store, err := local.NewStore(tempDir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create content store")
	}

	// Create in-memory image store
	imageStore := &memoryImageStore{
		images: make(map[string]images.Image),
	}

	// Open tar file
	tarFile, err := os.Open(tarPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open tar file %s", tarPath)
	}
	defer tarFile.Close()

	// Import tar into content store  
	log.G(ctx).Infof("importing tar file: %s", tarPath)
	indexDesc, err := archive.ImportIndex(ctx, store, tarFile,
		archive.WithImportCompression(),
	)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to import tar file")
	}

	// Read the actual index content from the content store
	indexContent, err := content.ReadBlob(ctx, store, indexDesc)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read imported index")
	}

	var index v1.Index
	if err := json.Unmarshal(indexContent, &index); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal index")
	}

	// Store image references (skip provenance layers)
	log.G(ctx).Infof("processing %d manifests from index", len(index.Manifests))
	for i, manifest := range index.Manifests {
		log.G(ctx).Infof("=== Processing manifest %d/%d ===", i+1, len(index.Manifests))
		log.G(ctx).Infof("  MediaType: %s", manifest.MediaType)
		log.G(ctx).Infof("  Digest: %s", manifest.Digest)
		log.G(ctx).Infof("  Size: %d bytes", manifest.Size)
		
		if manifest.Platform != nil {
			log.G(ctx).Infof("  Platform: %s/%s", manifest.Platform.OS, manifest.Platform.Architecture)
			if manifest.Platform.Variant != "" {
				log.G(ctx).Infof("  Platform Variant: %s", manifest.Platform.Variant)
			}
		}
		
		if manifest.ArtifactType != "" {
			log.G(ctx).Infof("  ArtifactType: %s", manifest.ArtifactType)
		}
		
		if manifest.Annotations != nil && len(manifest.Annotations) > 0 {
			log.G(ctx).Infof("  Annotations:")
			for key, value := range manifest.Annotations {
				log.G(ctx).Infof("    %s: %s", key, value)
			}
		}

		// Skip provenance and attestation manifests
		if isProvenanceManifestWithContent(ctx, store, manifest) {
			log.G(ctx).Infof("  ❌ SKIPPING: Detected as provenance/attestation manifest")
			continue
		}

		// Check if this is an image index (multi-arch) 
		if manifest.MediaType == v1.MediaTypeImageIndex || manifest.MediaType == "application/vnd.docker.distribution.manifest.list.v2+json" {
			log.G(ctx).Infof("  📁 FOUND IMAGE INDEX: Importing as-is for multi-arch conversion")
			
			// Import the index itself - let the builder handle platform traversal
			ref := fmt.Sprintf("imported:%s", manifest.Digest.Encoded()[:12])
			if manifest.Annotations != nil {
				if name, ok := manifest.Annotations["org.opencontainers.image.ref.name"]; ok {
					ref = name
					log.G(ctx).Infof("  Using annotation-based ref: %s", ref)
				}
			}

			image := images.Image{
				Name:   ref,
				Target: manifest,
			}
			imageStore.Create(ctx, image)
			log.G(ctx).Infof("  ✅ IMPORTED INDEX: %s -> %s (will be processed as multi-arch)", ref, manifest.Digest)
			continue
		}

		log.G(ctx).Infof("  ✅ PROCESSING: Regular container manifest")

		// Generate a reference name for the imported image
		ref := fmt.Sprintf("imported:%s", manifest.Digest.Encoded()[:12])
		if manifest.Annotations != nil {
			if name, ok := manifest.Annotations["org.opencontainers.image.ref.name"]; ok {
				ref = name
				log.G(ctx).Infof("  Using annotation-based ref: %s", ref)
			}
		}

		image := images.Image{
			Name:   ref,
			Target: manifest,
		}
		imageStore.Create(ctx, image)
		log.G(ctx).Infof("  ✅ IMPORTED: %s -> %s", ref, manifest.Digest)
	}

	return NewContentStoreResolver(store, imageStore), nil
}

// Resolve resolves a reference to a descriptor
func (r *ContentStoreResolver) Resolve(ctx context.Context, ref string) (string, v1.Descriptor, error) {
	log.G(ctx).Debugf("resolving reference: %s", ref)

	// Parse reference
	name, tag := parseRef(ref)
	
	// Look up in image store
	image, err := r.imageStore.Get(ctx, name)
	if err != nil {
		// Try with tag appended
		if tag != "" {
			image, err = r.imageStore.Get(ctx, ref)
		}
		if err != nil {
			return "", v1.Descriptor{}, errors.Wrapf(errdefs.ErrNotFound, "image not found: %s", ref)
		}
	}

	return ref, image.Target, nil
}

// Fetcher returns a fetcher for the content store
func (r *ContentStoreResolver) Fetcher(ctx context.Context, ref string) (remotes.Fetcher, error) {
	return &ContentStoreFetcher{
		store: r.store,
	}, nil
}

// Pusher returns a pusher for the content store
func (r *ContentStoreResolver) Pusher(ctx context.Context, ref string) (remotes.Pusher, error) {
	return &ContentStorePusher{
		store:      r.store,
		imageStore: r.imageStore,
		ref:        ref,
		tracker:    r.tracker,
	}, nil
}

// ContentStoreFetcher implements remotes.Fetcher using a content store
type ContentStoreFetcher struct {
	store content.Store
}

// Fetch fetches content from the content store
func (f *ContentStoreFetcher) Fetch(ctx context.Context, desc v1.Descriptor) (io.ReadCloser, error) {
	log.G(ctx).Debugf("fetching blob: %s", desc.Digest)

	reader, err := f.store.ReaderAt(ctx, desc)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get reader for %s", desc.Digest)
	}

	return &readerAtCloser{ReaderAt: reader, offset: 0}, nil
}

// ContentStorePusher implements remotes.Pusher using a content store
type ContentStorePusher struct {
	store      content.Store
	imageStore images.Store
	ref        string
	tracker    docker.StatusTracker
}

// Push pushes content to the content store
func (p *ContentStorePusher) Push(ctx context.Context, desc v1.Descriptor) (content.Writer, error) {
	log.G(ctx).Debugf("pushing blob: %s", desc.Digest)

	// Check if content already exists
	_, err := p.store.Info(ctx, desc.Digest)
	if err == nil {
		log.G(ctx).Debugf("content already exists: %s", desc.Digest)
		return &nopWriter{desc: desc}, nil
	}

	// Create writer
	writer, err := p.store.Writer(ctx, content.WithRef(remotes.MakeRefKey(ctx, desc)), content.WithDescriptor(desc))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create writer for %s", desc.Digest)
	}

	return &trackingWriter{
		Writer:  writer,
		desc:    desc,
		tracker: p.tracker,
		ref:     remotes.MakeRefKey(ctx, desc),
	}, nil
}

// Writer returns a content writer (not used by overlaybd conversion)
func (p *ContentStorePusher) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	return nil, errors.New("Writer not implemented")
}

// Helper types and functions

// readerAtCloser wraps content.ReaderAt to implement io.ReadCloser
type readerAtCloser struct {
	content.ReaderAt
	offset int64
}

func (r *readerAtCloser) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, r.offset)
	r.offset += int64(n)
	return n, err
}

func (r *readerAtCloser) Close() error {
	return r.ReaderAt.Close()
}

// nopWriter implements content.Writer for already existing content
type nopWriter struct {
	desc v1.Descriptor
}

func (w *nopWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *nopWriter) Close() error {
	return nil
}

func (w *nopWriter) Digest() digest.Digest {
	return w.desc.Digest
}

func (w *nopWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	return nil
}

func (w *nopWriter) Status() (content.Status, error) {
	return content.Status{
		Ref:    w.desc.Digest.String(),
		Offset: w.desc.Size,
		Total:  w.desc.Size,
	}, nil
}

func (w *nopWriter) Truncate(size int64) error {
	return nil
}

// trackingWriter wraps content.Writer with progress tracking
type trackingWriter struct {
	content.Writer
	desc    v1.Descriptor
	tracker docker.StatusTracker
	ref     string
}

func (w *trackingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil && w.tracker != nil {
		status, _ := w.tracker.GetStatus(w.ref)
		status.Offset += int64(n)
		status.UpdatedAt = time.Now()
		w.tracker.SetStatus(w.ref, status)
	}
	return n, err
}

func (w *trackingWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	err := w.Writer.Commit(ctx, size, expected, opts...)
	if err == nil && w.tracker != nil {
		status, _ := w.tracker.GetStatus(w.ref)
		status.Committed = true
		status.UpdatedAt = time.Now()
		w.tracker.SetStatus(w.ref, status)
	}
	return err
}

// memoryImageStore implements images.Store in memory
type memoryImageStore struct {
	images map[string]images.Image
}

func (s *memoryImageStore) Get(ctx context.Context, name string) (images.Image, error) {
	if img, ok := s.images[name]; ok {
		return img, nil
	}
	return images.Image{}, errdefs.ErrNotFound
}

func (s *memoryImageStore) List(ctx context.Context, filters ...string) ([]images.Image, error) {
	var result []images.Image
	for _, img := range s.images {
		result = append(result, img)
	}
	return result, nil
}

func (s *memoryImageStore) Create(ctx context.Context, image images.Image) (images.Image, error) {
	s.images[image.Name] = image
	return image, nil
}

func (s *memoryImageStore) Update(ctx context.Context, image images.Image, fieldpaths ...string) (images.Image, error) {
	s.images[image.Name] = image
	return image, nil
}

func (s *memoryImageStore) Delete(ctx context.Context, name string, opts ...images.DeleteOpt) error {
	delete(s.images, name)
	return nil
}

// parseRef parses a reference into name and tag
func parseRef(ref string) (name, tag string) {
	if idx := strings.LastIndex(ref, ":"); idx != -1 {
		return ref[:idx], ref[idx+1:]
	}
	return ref, ""
}

// ExportContentStoreToTar exports a content store to a tar file
func ExportContentStoreToTar(ctx context.Context, store content.Store, imageStore images.Store, tarPath string) error {
	// Create tar file
	if err := os.MkdirAll(filepath.Dir(tarPath), 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory for tar file")
	}

	tarFile, err := os.Create(tarPath)
	if err != nil {
		return errors.Wrapf(err, "failed to create tar file %s", tarPath)
	}
	defer tarFile.Close()

	// List all images
	imageList, err := imageStore.List(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to list images")
	}

	if len(imageList) == 0 {
		return errors.New("no images to export")
	}

	log.G(ctx).Infof("exporting %d images to tar file: %s", len(imageList), tarPath)

	// Export to tar with each image as a separate option
	var exportOpts []archive.ExportOpt
	for _, img := range imageList {
		exportOpts = append(exportOpts, archive.WithImage(imageStore, img.Name))
	}
	exportOpts = append(exportOpts, archive.WithAllPlatforms())

	return archive.Export(ctx, store, tarFile, exportOpts...)
}

// isProvenanceManifest checks if a manifest descriptor represents provenance/attestation metadata
func isProvenanceManifest(desc v1.Descriptor) bool {
	// Check for common provenance and attestation media types
	provenanceTypes := []string{
		"application/vnd.in-toto+json",
		"application/vnd.dev.cosign.simplesigning.v1+json",
		"application/vnd.dev.sigstore.bundle+json",
	}

	for _, pType := range provenanceTypes {
		if strings.Contains(desc.MediaType, pType) {
			log.L.Debugf("🔍 FILTERING: MediaType contains provenance type: %s", pType)
			return true
		}
	}

	// Check for provenance-related artifact types
	if desc.ArtifactType != "" {
		if strings.Contains(desc.ArtifactType, "provenance") ||
			strings.Contains(desc.ArtifactType, "attestation") ||
			strings.Contains(desc.ArtifactType, "signature") {
			log.L.Debugf("🔍 FILTERING: ArtifactType contains provenance marker: %s", desc.ArtifactType)
			return true
		}
	}

	// Check annotations for provenance markers
	if desc.Annotations != nil {
		if _, exists := desc.Annotations["in-toto.io/predicate-type"]; exists {
			log.L.Debugf("🔍 FILTERING: Found in-toto.io/predicate-type annotation")
			return true
		}
		if _, exists := desc.Annotations["vnd.docker.reference.type"]; exists {
			if refType := desc.Annotations["vnd.docker.reference.type"]; refType == "attestation-manifest" || strings.Contains(refType, "provenance") {
				log.L.Debugf("🔍 FILTERING: Found vnd.docker.reference.type: %s", refType)
				return true
			}
		}
	}

	log.L.Debugf("🔍 NOT FILTERING: No provenance markers found for digest: %s", desc.Digest.Encoded()[:12])
	return false
}

// isProvenanceManifestWithContent checks if a manifest descriptor represents provenance/attestation metadata
// by examining both the descriptor metadata and the actual manifest content
func isProvenanceManifestWithContent(ctx context.Context, store content.Store, desc v1.Descriptor) bool {
	log.G(ctx).Debugf("    🔍 Checking if manifest is provenance: %s", desc.Digest)
	
	// First check descriptor metadata
	if isProvenanceManifest(desc) {
		log.G(ctx).Debugf("    🔍 Detected provenance via descriptor metadata")
		return true
	}

	// For generic manifest media types, we need to check the content
	if desc.MediaType == "application/vnd.docker.distribution.manifest.v2+json" ||
		desc.MediaType == "application/vnd.oci.image.manifest.v1+json" {
		
		log.G(ctx).Debugf("    🔍 Generic manifest media type, reading content to inspect...")
		
		// Read the manifest content
		manifestContent, err := content.ReadBlob(ctx, store, desc)
		if err != nil {
			log.G(ctx).Debugf("    🔍 Failed to read manifest content: %v", err)
			// If we can't read it, assume it's not provenance
			return false
		}

		// Show first 200 chars of content for debugging
		contentStr := string(manifestContent)
		preview := contentStr
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		log.G(ctx).Debugf("    🔍 Content preview: %s", preview)

		// Check if content contains in-toto attestation markers
		if strings.Contains(contentStr, `"_type":"https://in-toto.io/`) ||
			strings.Contains(contentStr, `"predicateType":"https://spdx.dev/`) ||
			strings.Contains(contentStr, `"predicateType":"https://slsa.dev/`) ||
			strings.Contains(contentStr, `"predicateType":"https://in-toto.io/`) {
			log.G(ctx).Debugf("    🔍 FOUND provenance markers in content!")
			return true
		}
		
		log.G(ctx).Debugf("    🔍 No provenance markers found in content")
	} else {
		log.G(ctx).Debugf("    🔍 Non-generic media type, skipping content inspection")
	}

	return false
}