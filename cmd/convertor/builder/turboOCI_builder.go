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
	"path"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/accelerated-container-image/pkg/snapshot"
	"github.com/containerd/accelerated-container-image/pkg/utils"
	"github.com/containerd/accelerated-container-image/pkg/version"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (
	// index of OCI layers (gzip)
	gzipMetaFile = "gzip.meta"

	// index of block device
	fsMetaFile = "ext4.fs.meta"

	// foci index layer (gzip)
	tociLayerTar = "turboOCIv1.tar.gz"

	// tociIdentifier is an empty file just used as a identifier
	tociIdentifier = ".turbo.ociv1"
)

type turboOCIBuilderEngine struct {
	*builderEngineBase
	overlaybdConfig *snapshot.OverlayBDBSConfig
	tociLayers      []specs.Descriptor
	isGzip          []bool
}

func NewTurboOCIBuilderEngine(base *builderEngineBase) builderEngine {
	config := &snapshot.OverlayBDBSConfig{
		Lowers:     []snapshot.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	if !base.mkfs {
		config.Lowers = append(config.Lowers, snapshot.OverlayBDBSConfigLower{
			File: overlaybdBaseLayer,
		})
		logrus.Infof("using default baselayer")
	}
	return &turboOCIBuilderEngine{
		builderEngineBase: base,
		overlaybdConfig:   config,
		tociLayers:        make([]specs.Descriptor, len(base.manifest.Layers)),
		isGzip:            make([]bool, len(base.manifest.Layers)),
	}
}

func (e *turboOCIBuilderEngine) DownloadLayer(ctx context.Context, idx int) error {
	var err error
	if e.isGzip[idx], err = e.isGzipLayer(ctx, idx); err != nil {
		return err
	}

	desc := e.manifest.Layers[idx]
	targetFile := path.Join(e.getLayerDir(idx), "layer.tar")
	return downloadLayer(ctx, e.fetcher, targetFile, desc, false)
}

func (e *turboOCIBuilderEngine) BuildLayer(ctx context.Context, idx int) error {
	layerDir := e.getLayerDir(idx)
	if err := e.create(ctx, layerDir, e.mkfs && (idx == 0)); err != nil {
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
		return errors.Wrapf(err, "failed to create identifier %q", tociIdentifier)
	}
	files := []string{
		path.Join(layerDir, fsMetaFile),
		path.Join(layerDir, tociIdentifier),
	}
	gzipIndexPath := ""
	if e.isGzip[idx] {
		gzipIndexPath = path.Join(layerDir, gzipMetaFile)
		files = append(files, gzipIndexPath)
	}
	if err := buildArchiveFromFiles(ctx, path.Join(layerDir, tociLayerTar), compression.Gzip, files...); err != nil {
		return errors.Wrapf(err, "failed to create turboOCIv1 archive for layer %d", idx)
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

func (e *turboOCIBuilderEngine) UploadLayer(ctx context.Context, idx int) error {
	layerDir := e.getLayerDir(idx)
	desc, err := getFileDesc(path.Join(layerDir, tociLayerTar), false)
	if err != nil {
		return errors.Wrapf(err, "failed to get descriptor for layer %d", idx)
	}
	desc.MediaType = e.mediaTypeImageLayerGzip()
	desc.Annotations = map[string]string{
		label.OverlayBDVersion:    version.TurboOCIVersionNumber,
		label.OverlayBDBlobDigest: desc.Digest.String(),
		label.OverlayBDBlobSize:   fmt.Sprintf("%d", desc.Size),
		label.TurboOCIDigest:      e.manifest.Layers[idx].Digest.String(),
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
	desc.Annotations[label.TurboOCIMediaType] = targetMediaType
	if err := uploadBlob(ctx, e.pusher, path.Join(layerDir, tociLayerTar), desc); err != nil {
		return errors.Wrapf(err, "failed to upload layer %d", idx)
	}
	e.tociLayers[idx] = desc
	return nil
}

func (e *turboOCIBuilderEngine) UploadImage(ctx context.Context) error {
	for idx := range e.manifest.Layers {
		layerDir := e.getLayerDir(idx)
		uncompress, err := getFileDesc(path.Join(layerDir, tociLayerTar), true)
		if err != nil {
			return errors.Wrapf(err, "failed to get uncompressed descriptor for layer %d", idx)
		}
		e.manifest.Layers[idx] = e.tociLayers[idx]
		e.config.RootFS.DiffIDs[idx] = uncompress.Digest
	}
	baseDesc := specs.Descriptor{
		MediaType: e.mediaTypeImageLayer(),
		Digest:    "sha256:c3a417552a6cf9ffa959b541850bab7d7f08f4255425bf8b48c85f7b36b378d9",
		Size:      4737695,
		Annotations: map[string]string{
			label.OverlayBDVersion:    version.OverlayBDVersionNumber,
			label.OverlayBDBlobDigest: "sha256:c3a417552a6cf9ffa959b541850bab7d7f08f4255425bf8b48c85f7b36b378d9",
			label.OverlayBDBlobSize:   "4737695",
		},
	}
	if !e.mkfs {
		if err := uploadBlob(ctx, e.pusher, overlaybdBaseLayer, baseDesc); err != nil {
			return errors.Wrapf(err, "failed to upload baselayer %q", overlaybdBaseLayer)
		}
		e.manifest.Layers = append([]specs.Descriptor{baseDesc}, e.manifest.Layers...)
		e.config.RootFS.DiffIDs = append([]digest.Digest{baseDesc.Digest}, e.config.RootFS.DiffIDs...)
	}
	return e.uploadManifestAndConfig(ctx)
}

// Layer deduplication in FastOCI is not currently supported due to conversion not
// being reproducible at the moment which can lead to occasional bugs.

// CheckForConvertedLayer TODO
func (e *turboOCIBuilderEngine) CheckForConvertedLayer(ctx context.Context, idx int) (specs.Descriptor, error) {
	return specs.Descriptor{}, errdefs.ErrNotFound
}

// StoreConvertedLayerDetails TODO
func (e *turboOCIBuilderEngine) StoreConvertedLayerDetails(ctx context.Context, idx int) error {
	return nil
}

// DownloadConvertedLayer TODO
func (e *turboOCIBuilderEngine) DownloadConvertedLayer(ctx context.Context, idx int, desc specs.Descriptor) error {
	return errdefs.ErrNotImplemented
}

func (e *turboOCIBuilderEngine) Cleanup() {
	os.RemoveAll(e.workDir)
}

func (e *turboOCIBuilderEngine) getLayerDir(idx int) string {
	return path.Join(e.workDir, fmt.Sprintf("%04d_", idx)+e.manifest.Layers[idx].Digest.String())
}

func (e *turboOCIBuilderEngine) createIdentifier(idx int) error {
	targetFile := path.Join(e.getLayerDir(idx), tociIdentifier)
	file, err := os.Create(targetFile)
	if err != nil {
		return errors.Wrapf(err, "failed to create identifier file %q", tociIdentifier)
	}
	defer file.Close()
	return nil
}

func (e *turboOCIBuilderEngine) create(ctx context.Context, dir string, mkfs bool) error {
	vsizeGB := 64
	if mkfs {
		vsizeGB = e.vsize
	}
	opts := []string{"-s", fmt.Sprintf("%d", vsizeGB), "--turboOCI"}
	if mkfs {
		opts = append(opts, "--mkfs")
		logrus.Infof("mkfs for baselayer, vsize: %d GB", vsizeGB)
	}
	return utils.Create(ctx, dir, opts...)
}

func (e *turboOCIBuilderEngine) apply(ctx context.Context, dir string) error {
	return utils.ApplyTurboOCI(ctx, dir, gzipMetaFile)
}

func (e *turboOCIBuilderEngine) commit(ctx context.Context, dir string) error {
	if err := utils.Commit(ctx, dir, dir, false, "-z", "--fastoci"); err != nil {
		return err
	}
	return os.Rename(path.Join(dir, commitFile), path.Join(dir, fsMetaFile))
}
