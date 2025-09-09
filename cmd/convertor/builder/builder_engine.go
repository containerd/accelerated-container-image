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
	"path"

	"github.com/containerd/accelerated-container-image/cmd/convertor/database"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/containerd/continuity"
	"github.com/containerd/log"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

type BuilderEngineType int

const (
	Overlaybd BuilderEngineType = iota
	TurboOCI
)

const (
	ArtifactTypeOverlaybd = "application/vnd.containerd.overlaybd.native.v1+json"
	ArtifactTypeTurboOCI  = "application/vnd.containerd.overlaybd.turbo.v1+json"
)

func (engine BuilderEngineType) ArtifactType() string {
	switch engine {
	case Overlaybd:
		return ArtifactTypeOverlaybd
	case TurboOCI:
		return ArtifactTypeTurboOCI
	default:
		return ""
	}
}

type builderEngine interface {
	DownloadLayer(ctx context.Context, idx int) error

	// build layer archive, maybe tgz or zfile
	BuildLayer(ctx context.Context, idx int) error

	UploadLayer(ctx context.Context, idx int) error

	// UploadImage upload new manifest and config, return the descriptor of the manifest
	UploadImage(ctx context.Context) (specs.Descriptor, error)

	// Cleanup removes workdir
	Cleanup()

	Deduplicateable
}

// Deduplicateable provides a number of functions to avoid duplicating work when converting images
// It is used by the builderEngine to avoid re-converting layers and manifests
type Deduplicateable interface {
	// deduplication functions
	// finds already converted layer in db and validates presence in registry
	CheckForConvertedLayer(ctx context.Context, idx int) (specs.Descriptor, error)

	// downloads the already converted layer
	DownloadConvertedLayer(ctx context.Context, idx int, desc specs.Descriptor) error

	// store chainID -> converted layer mapping for layer deduplication
	StoreConvertedLayerDetails(ctx context.Context, idx int) error

	// store manifest digest -> converted manifest to avoid re-conversion
	CheckForConvertedManifest(ctx context.Context) (specs.Descriptor, error)

	// tag a converted manifest -> converted manifest to avoid re-conversion
	TagPreviouslyConvertedManifest(ctx context.Context, desc specs.Descriptor) error

	// store manifest digest -> converted manifest to avoid re-conversion
	StoreConvertedManifestDetails(ctx context.Context) error
}

type builderEngineBase struct {
	resolver     remotes.Resolver
	fetcher      remotes.Fetcher
	pusher       remotes.Pusher
	manifest     specs.Manifest
	config       specs.Image
	workDir      string
	oci          bool
	fstype       string
	mkfs         bool
	vsize        int
	db           database.ConversionDatabase
	host         string
	repository   string
	inputDesc    specs.Descriptor // original manifest descriptor
	outputDesc   specs.Descriptor // converted manifest descriptor
	reserve      bool
	noUpload     bool
	dumpManifest bool
	referrer     bool
}

func (e *builderEngineBase) isGzipLayer(ctx context.Context, idx int) (bool, error) {
	rc, err := e.fetcher.Fetch(ctx, e.manifest.Layers[idx])
	if err != nil {
		return false, fmt.Errorf("isGzipLayer: failed to open layer %d: %w", idx, err)
	}
	drc, err := compression.DecompressStream(rc)
	if err != nil {
		return false, fmt.Errorf("isGzipLayer: failed to open decompress stream for layer %d: %w", idx, err)
	}
	compress := drc.GetCompression()
	switch compress {
	case compression.Uncompressed:
		return false, nil
	case compression.Gzip:
		return true, nil
	default:
		return false, fmt.Errorf("isGzipLayer: unsupported layer format with compression %s", compress.Extension())
	}
}

func (e *builderEngineBase) mediaTypeManifest() string {
	if e.oci {
		return specs.MediaTypeImageManifest
	} else {
		return images.MediaTypeDockerSchema2Manifest
	}
}

func (e *builderEngineBase) mediaTypeConfig() string {
	if e.oci {
		return specs.MediaTypeImageConfig
	} else {
		return images.MediaTypeDockerSchema2Config
	}
}

func (e *builderEngineBase) mediaTypeImageLayerGzip() string {
	if e.oci {
		return specs.MediaTypeImageLayerGzip
	} else {
		return images.MediaTypeDockerSchema2LayerGzip
	}
}

func (e *builderEngineBase) mediaTypeImageLayer() string {
	if e.oci {
		return specs.MediaTypeImageLayer
	} else {
		return images.MediaTypeDockerSchema2Layer
	}
}

func (e *builderEngineBase) uploadManifestAndConfig(ctx context.Context) (specs.Descriptor, error) {
	cbuf, err := json.Marshal(e.config)
	if err != nil {
		return specs.Descriptor{}, err
	}
	e.manifest.Config = specs.Descriptor{
		MediaType: e.mediaTypeConfig(),
		Digest:    digest.FromBytes(cbuf),
		Size:      (int64)(len(cbuf)),
	}
	if !e.noUpload {
		if err = uploadBytes(ctx, e.pusher, e.manifest.Config, cbuf); err != nil {
			return specs.Descriptor{}, fmt.Errorf("failed to upload config: %w", err)
		}
		log.G(ctx).Infof("config uploaded")
	}
	if e.dumpManifest {
		confPath := path.Join(e.workDir, "config.json")
		if err := continuity.AtomicWriteFile(confPath, cbuf, 0644); err != nil {
			return specs.Descriptor{}, err
		}
		log.G(ctx).Infof("config dumped")
	}

	e.manifest.MediaType = e.mediaTypeManifest()
	cbuf, err = json.Marshal(e.manifest)
	if err != nil {
		return specs.Descriptor{}, err
	}
	manifestDesc := specs.Descriptor{
		MediaType: e.mediaTypeManifest(),
		Digest:    digest.FromBytes(cbuf),
		Size:      (int64)(len(cbuf)),
	}
	if !e.noUpload {
		if err = uploadBytes(ctx, e.pusher, manifestDesc, cbuf); err != nil {
			return specs.Descriptor{}, fmt.Errorf("failed to upload manifest: %w", err)
		}
		e.outputDesc = manifestDesc
		log.G(ctx).Infof("manifest uploaded, %s", manifestDesc.Digest)
	}
	if e.dumpManifest {
		descPath := path.Join(e.workDir, "manifest.json")
		if err := continuity.AtomicWriteFile(descPath, cbuf, 0644); err != nil {
			return specs.Descriptor{}, err
		}
		log.G(ctx).Infof("manifest dumped")
	}
	return manifestDesc, nil
}

func getBuilderEngineBase(ctx context.Context, resolver remotes.Resolver, ref, targetRef string) (*builderEngineBase, error) {
	_, desc, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve reference %q: %w", ref, err)
	}
	fetcher, err := resolver.Fetcher(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to get fetcher for %q: %w", ref, err)
	}
	pusher, err := resolver.Pusher(ctx, targetRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get pusher for %q: %w", targetRef, err)
	}
	manifest, config, err := fetchManifestAndConfig(ctx, fetcher, desc)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manifest and config: %w", err)
	}
	return &builderEngineBase{
		resolver:  resolver,
		fetcher:   fetcher,
		pusher:    pusher,
		manifest:  *manifest,
		config:    *config,
		inputDesc: desc,
	}, nil
}
