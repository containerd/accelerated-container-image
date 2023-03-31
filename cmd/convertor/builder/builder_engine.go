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

	"github.com/containerd/accelerated-container-image/cmd/convertor/database"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/remotes"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type BuilderEngineType int

const (
	BuilderEngineTypeOverlayBD BuilderEngineType = iota
	BuilderEngineTypeFastOCI
)

type builderEngine interface {
	DownloadLayer(ctx context.Context, idx int) error

	// build layer archive, maybe tgz or zfile
	BuildLayer(ctx context.Context, idx int) error

	UploadLayer(ctx context.Context, idx int) error

	// UploadImage upload new manifest and config
	UploadImage(ctx context.Context) error

	// deduplication functions
	// finds already converted layer in db and validates presence in registry
	CheckForConvertedLayer(ctx context.Context, idx int) (specs.Descriptor, error)

	// downloads the already converted layer
	DownloadConvertedLayer(ctx context.Context, idx int, desc specs.Descriptor) error

	// store chainID -> converted layer mapping for deduplication
	StoreConvertedLayerDetails(ctx context.Context, idx int) error

	// Cleanup removes workdir
	Cleanup()
}

type builderEngineBase struct {
	fetcher    remotes.Fetcher
	pusher     remotes.Pusher
	manifest   specs.Manifest
	config     specs.Image
	workDir    string
	oci        bool
	db         database.ConversionDatabase
	host       string
	repository string
}

func (e *builderEngineBase) isGzipLayer(ctx context.Context, idx int) (bool, error) {
	rc, err := e.fetcher.Fetch(ctx, e.manifest.Layers[idx])
	if err != nil {
		return false, errors.Wrapf(err, "isGzipLayer: failed to open layer %d", idx)
	}
	drc, err := compression.DecompressStream(rc)
	if err != nil {
		return false, errors.Wrapf(err, "isGzipLayer: failed to open decompress stream for layer %d", idx)
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

func (e *builderEngineBase) uploadManifestAndConfig(ctx context.Context) error {
	cbuf, err := json.Marshal(e.config)
	if err != nil {
		return err
	}
	e.manifest.Config = specs.Descriptor{
		MediaType: e.mediaTypeConfig(),
		Digest:    digest.FromBytes(cbuf),
		Size:      (int64)(len(cbuf)),
	}
	if err = uploadBytes(ctx, e.pusher, e.manifest.Config, cbuf); err != nil {
		return errors.Wrapf(err, "failed to upload config")
	}
	logrus.Infof("config uploaded")

	e.manifest.MediaType = e.mediaTypeManifest()
	cbuf, err = json.Marshal(e.manifest)
	if err != nil {
		return err
	}
	manifestDesc := specs.Descriptor{
		MediaType: e.mediaTypeManifest(),
		Digest:    digest.FromBytes(cbuf),
		Size:      (int64)(len(cbuf)),
	}

	if err = uploadBytes(ctx, e.pusher, manifestDesc, cbuf); err != nil {
		return errors.Wrapf(err, "failed to upload manifest")
	}
	logrus.Infof("manifest uploaded")

	return nil
}

func getBuilderEngineBase(ctx context.Context, resolver remotes.Resolver, ref, targetRef string) (*builderEngineBase, error) {
	_, desc, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to resolve reference %q", ref)
	}
	fetcher, err := resolver.Fetcher(ctx, ref)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get fetcher for %q", ref)
	}
	pusher, err := resolver.Pusher(ctx, targetRef)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get pusher for %q", targetRef)
	}
	manifest, config, err := fetchManifestAndConfig(ctx, fetcher, desc)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch manifest and config")
	}
	return &builderEngineBase{
		fetcher:  fetcher,
		pusher:   pusher,
		manifest: manifest,
		config:   config,
	}, nil
}
