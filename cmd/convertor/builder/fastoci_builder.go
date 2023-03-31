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
	"os/exec"
	"path"
	"path/filepath"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/accelerated-container-image/pkg/snapshot"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	// index of OCI layers (gzip)
	gzipMetaFile = "gzip.meta"

	// index of block device
	fsMetaFile = "ext4.fs.meta"

	// foci index layer (gzip)
	fociLayerTar = "foci.tar.gz"

	// fociIdentifier is an empty file just used as a identifier
	fociIdentifier = ".fastoci.overlaybd"
)

type fastOCIBuilderEngine struct {
	*builderEngineBase
	overlaybdConfig *snapshot.OverlayBDBSConfig
	fociLayers      []specs.Descriptor
	isGzip          []bool
}

func NewFastOCIBuilderEngine(base *builderEngineBase) builderEngine {
	config := &snapshot.OverlayBDBSConfig{
		Lowers:     []snapshot.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	config.Lowers = append(config.Lowers, snapshot.OverlayBDBSConfigLower{
		File: overlaybdBaseLayer,
	})
	return &fastOCIBuilderEngine{
		builderEngineBase: base,
		overlaybdConfig:   config,
		fociLayers:        make([]specs.Descriptor, len(base.manifest.Layers)),
		isGzip:            make([]bool, len(base.manifest.Layers)),
	}
}

func (e *fastOCIBuilderEngine) DownloadLayer(ctx context.Context, idx int) error {
	var err error
	if e.isGzip[idx], err = e.isGzipLayer(ctx, idx); err != nil {
		return err
	}

	desc := e.manifest.Layers[idx]
	targetFile := path.Join(e.getLayerDir(idx), "layer.tar")
	return downloadLayer(ctx, e.fetcher, targetFile, desc, false)
}

func (e *fastOCIBuilderEngine) BuildLayer(ctx context.Context, idx int) error {
	layerDir := e.getLayerDir(idx)
	if err := e.create(ctx, layerDir); err != nil {
		return err
	}
	e.overlaybdConfig.Upper = snapshot.OverlayBDBSConfigUpper{
		Data:   path.Join(layerDir, "writable_data"),
		Index:  path.Join(layerDir, "writable_index"),
		Target: path.Join(layerDir, "layer.tar"),
	}
	if err := writeConfig(layerDir, e.overlaybdConfig); err != nil {
		return err
	}
	if err := e.apply(ctx, layerDir); err != nil {
		return err
	}
	if err := e.commit(ctx, layerDir); err != nil {
		return err
	}
	if err := e.createIdentifier(idx); err != nil {
		return errors.Wrapf(err, "failed to create identifier %q", fociIdentifier)
	}
	files := []string{
		path.Join(layerDir, fsMetaFile),
		path.Join(layerDir, fociIdentifier),
	}
	gzipIndexPath := ""
	if e.isGzip[idx] {
		gzipIndexPath = path.Join(layerDir, gzipMetaFile)
		files = append(files, gzipIndexPath)
	}
	if err := buildArchiveFromFiles(ctx, path.Join(layerDir, fociLayerTar), compression.Gzip, files...); err != nil {
		return errors.Wrapf(err, "failed to create foci archive for layer %d", idx)
	}
	e.overlaybdConfig.Lowers = append(e.overlaybdConfig.Lowers, snapshot.OverlayBDBSConfigLower{
		TargetFile:   path.Join(layerDir, "layer.tar"),
		TargetDigest: string(e.manifest.Layers[idx].Digest), // TargetDigest should be set to work with gzip cache
		File:         path.Join(layerDir, fsMetaFile),
		GzipIndex:    gzipIndexPath,
	})
	os.Remove(path.Join(layerDir, "writable_data"))
	os.Remove(path.Join(layerDir, "writable_index"))
	return nil
}

func (e *fastOCIBuilderEngine) UploadLayer(ctx context.Context, idx int) error {
	layerDir := e.getLayerDir(idx)
	desc, err := getFileDesc(path.Join(layerDir, fociLayerTar), false)
	if err != nil {
		return errors.Wrapf(err, "failed to get descriptor for layer %d", idx)
	}
	desc.MediaType = e.mediaTypeImageLayerGzip()
	desc.Annotations = map[string]string{
		label.OverlayBDBlobDigest: desc.Digest.String(),
		label.OverlayBDBlobSize:   fmt.Sprintf("%d", desc.Size),
		label.FastOCIDigest:       e.manifest.Layers[idx].Digest.String(),
	}
	targetMediaType := ""
	if images.IsDockerType(e.manifest.Layers[idx].MediaType) {
		if e.isGzip[idx] {
			targetMediaType = images.MediaTypeDockerSchema2LayerGzip
		} else {
			targetMediaType = images.MediaTypeDockerSchema2Layer
		}
	} else {
		if e.isGzip[idx] {
			targetMediaType = specs.MediaTypeImageLayerGzip
		} else {
			targetMediaType = specs.MediaTypeImageLayer
		}
	}
	desc.Annotations[label.FastOCIMediaType] = targetMediaType
	if err := uploadBlob(ctx, e.pusher, path.Join(layerDir, fociLayerTar), desc); err != nil {
		return errors.Wrapf(err, "failed to upload layer %d", idx)
	}
	e.fociLayers[idx] = desc
	return nil
}

func (e *fastOCIBuilderEngine) UploadImage(ctx context.Context) error {
	for idx := range e.manifest.Layers {
		layerDir := e.getLayerDir(idx)
		uncompress, err := getFileDesc(path.Join(layerDir, fociLayerTar), true)
		if err != nil {
			return errors.Wrapf(err, "failed to get uncompressed descriptor for layer %d", idx)
		}
		e.manifest.Layers[idx] = e.fociLayers[idx]
		e.config.RootFS.DiffIDs[idx] = uncompress.Digest
	}
	baseDesc := specs.Descriptor{
		MediaType: e.mediaTypeImageLayer(),
		Digest:    "sha256:c3a417552a6cf9ffa959b541850bab7d7f08f4255425bf8b48c85f7b36b378d9",
		Size:      4737695,
		Annotations: map[string]string{
			label.OverlayBDBlobDigest: "sha256:c3a417552a6cf9ffa959b541850bab7d7f08f4255425bf8b48c85f7b36b378d9",
			label.OverlayBDBlobSize:   "4737695",
		},
	}
	if err := uploadBlob(ctx, e.pusher, overlaybdBaseLayer, baseDesc); err != nil {
		return errors.Wrapf(err, "failed to upload baselayer %q", overlaybdBaseLayer)
	}
	e.manifest.Layers = append([]specs.Descriptor{baseDesc}, e.manifest.Layers...)
	e.config.RootFS.DiffIDs = append([]digest.Digest{baseDesc.Digest}, e.config.RootFS.DiffIDs...)
	return e.uploadManifestAndConfig(ctx)
}

// Layer deduplication in FastOCI is not currently supported due to conversion not
// being reproducible at the moment which can lead to occasional bugs.

// CheckForConvertedLayer TODO
func (e *fastOCIBuilderEngine) CheckForConvertedLayer(ctx context.Context, idx int) (specs.Descriptor, error) {
	return specs.Descriptor{}, errdefs.ErrNotFound
}

// StoreConvertedLayerDetails TODO
func (e *fastOCIBuilderEngine) StoreConvertedLayerDetails(ctx context.Context, idx int) error {
	return nil
}

// DownloadConvertedLayer TODO
func (e *fastOCIBuilderEngine) DownloadConvertedLayer(ctx context.Context, idx int, desc specs.Descriptor) error {
	return errdefs.ErrNotImplemented
}

func (e *fastOCIBuilderEngine) Cleanup() {
	os.RemoveAll(e.workDir)
}

func (e *fastOCIBuilderEngine) getLayerDir(idx int) string {
	return path.Join(e.workDir, fmt.Sprintf("%04d_", idx)+e.manifest.Layers[idx].Digest.String())
}

func (e *fastOCIBuilderEngine) createIdentifier(idx int) error {
	targetFile := path.Join(e.getLayerDir(idx), fociIdentifier)
	file, err := os.Create(targetFile)
	if err != nil {
		return errors.Wrapf(err, "failed to create identifier file %q", fociIdentifier)
	}
	defer file.Close()
	return nil
}

func (e *fastOCIBuilderEngine) create(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-create")
	dataPath := path.Join(dir, "writable_data")
	indexPath := path.Join(dir, "writable_index")
	os.RemoveAll(dataPath)
	os.RemoveAll(indexPath)
	out, err := exec.CommandContext(ctx, binpath, "-s",
		dataPath, indexPath, "64", "--fastoci").CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-create: %s", out)
	}
	return nil
}

func (e *fastOCIBuilderEngine) apply(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-apply")

	out, err := exec.CommandContext(ctx, binpath,
		path.Join(dir, "layer.tar"),
		path.Join(dir, "config.json"),
		"--gz_index_path", path.Join(dir, gzipMetaFile),
	).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-apply: %s", out)
	}
	return nil
}

func (e *fastOCIBuilderEngine) commit(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-commit")

	out, err := exec.CommandContext(ctx, binpath, "-z",
		path.Join(dir, "writable_data"),
		path.Join(dir, "writable_index"),
		path.Join(dir, fsMetaFile),
		"--fastoci",
	).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-commit: %s", out)
	}
	return nil
}
