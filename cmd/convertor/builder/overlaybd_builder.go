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
	"archive/tar"
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/accelerated-container-image/pkg/snapshot"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (
	overlaybdBaseLayer = "/opt/overlaybd/baselayers/ext4_64"

	commitFile = "overlaybd.commit"
)

type overlaybdBuilderEngine struct {
	*builderEngineBase
	overlaybdConfig *snapshot.OverlayBDBSConfig
	overlaybdLayers []specs.Descriptor
}

func NewOverlayBDBuilderEngine(base *builderEngineBase) builderEngine {
	config := &snapshot.OverlayBDBSConfig{
		Lowers:     []snapshot.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	config.Lowers = append(config.Lowers, snapshot.OverlayBDBSConfigLower{
		File: overlaybdBaseLayer,
	})
	return &overlaybdBuilderEngine{
		builderEngineBase: base,
		overlaybdConfig:   config,
		overlaybdLayers:   make([]specs.Descriptor, len(base.manifest.Layers)),
	}
}

func (e *overlaybdBuilderEngine) DownloadLayer(ctx context.Context, idx int) error {
	desc := e.manifest.Layers[idx]
	targetFile := path.Join(e.getLayerDir(idx), "layer.tar")
	return downloadLayer(ctx, e.fetcher, targetFile, desc, true)
}

func (e *overlaybdBuilderEngine) BuildLayer(ctx context.Context, idx int) error {
	layerDir := e.getLayerDir(idx)
	if err := e.create(ctx, layerDir); err != nil {
		return err
	}
	e.overlaybdConfig.Upper = snapshot.OverlayBDBSConfigUpper{
		Data:  path.Join(layerDir, "writable_data"),
		Index: path.Join(layerDir, "writable_index"),
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
	e.overlaybdConfig.Lowers = append(e.overlaybdConfig.Lowers, snapshot.OverlayBDBSConfigLower{
		File: path.Join(layerDir, commitFile),
	})
	os.Remove(path.Join(layerDir, "layer.tar"))
	os.Remove(path.Join(layerDir, "writable_data"))
	os.Remove(path.Join(layerDir, "writable_index"))
	return nil
}

func (e *overlaybdBuilderEngine) UploadLayer(ctx context.Context, idx int) error {
	layerDir := e.getLayerDir(idx)
	desc, err := getFileDesc(path.Join(layerDir, commitFile), false)
	if err != nil {
		return errors.Wrapf(err, "failed to get descriptor for layer %d", idx)
	}
	desc.MediaType = e.mediaTypeImageLayer()
	desc.Annotations = map[string]string{
		label.OverlayBDBlobDigest: desc.Digest.String(),
		label.OverlayBDBlobSize:   fmt.Sprintf("%d", desc.Size),
	}
	if err := uploadBlob(ctx, e.pusher, path.Join(layerDir, commitFile), desc); err != nil {
		return errors.Wrapf(err, "failed to upload layer %d", idx)
	}
	e.overlaybdLayers[idx] = desc
	return nil
}

func (e *overlaybdBuilderEngine) UploadImage(ctx context.Context) error {
	for idx := range e.manifest.Layers {
		e.manifest.Layers[idx] = e.overlaybdLayers[idx]
		e.config.RootFS.DiffIDs[idx] = e.overlaybdLayers[idx].Digest
	}
	baseDesc, err := e.uploadBaseLayer(ctx)
	if err != nil {
		return err
	}
	e.manifest.Layers = append([]specs.Descriptor{baseDesc}, e.manifest.Layers...)
	e.config.RootFS.DiffIDs = append([]digest.Digest{baseDesc.Digest}, e.config.RootFS.DiffIDs...)
	return e.uploadManifestAndConfig(ctx)
}

func (e *overlaybdBuilderEngine) Cleanup() {
	os.RemoveAll(e.workDir)
}

func (e *overlaybdBuilderEngine) uploadBaseLayer(ctx context.Context) (specs.Descriptor, error) {
	// add baselayer with tar header
	tarFile := path.Join(e.workDir, "ext4_64.tar")
	fdes, err := os.Create(tarFile)
	if err != nil {
		return specs.Descriptor{}, errors.Wrapf(err, "failed to create file %s", tarFile)
	}
	digester := digest.Canonical.Digester()
	countWriter := &writeCountWrapper{w: io.MultiWriter(fdes, digester.Hash())}
	tarWriter := tar.NewWriter(countWriter)

	fsrc, err := os.Open(overlaybdBaseLayer)
	if err != nil {
		return specs.Descriptor{}, errors.Wrapf(err, "failed to open %s", overlaybdBaseLayer)
	}
	fstat, err := os.Stat(overlaybdBaseLayer)
	if err != nil {
		return specs.Descriptor{}, errors.Wrapf(err, "failed to get info of %s", overlaybdBaseLayer)
	}

	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     commitFile,
		Mode:     0444,
		Size:     fstat.Size(),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return specs.Descriptor{}, errors.Wrapf(err, "failed to write tar header")
	}
	if _, err := io.Copy(tarWriter, bufio.NewReader(fsrc)); err != nil {
		return specs.Descriptor{}, errors.Wrapf(err, "failed to copy IO")
	}
	if err = tarWriter.Close(); err != nil {
		return specs.Descriptor{}, errors.Wrapf(err, "failed to close tar file")
	}

	baseDesc := specs.Descriptor{
		MediaType: e.mediaTypeImageLayer(),
		Digest:    digester.Digest(),
		Size:      countWriter.c,
		Annotations: map[string]string{
			"containerd.io/snapshot/overlaybd/blob-digest": digester.Digest().String(),
			"containerd.io/snapshot/overlaybd/blob-size":   fmt.Sprintf("%d", countWriter.c),
		},
	}
	if err = uploadBlob(ctx, e.pusher, tarFile, baseDesc); err != nil {
		return specs.Descriptor{}, errors.Wrapf(err, "failed to upload baselayer")
	}
	logrus.Infof("baselayer uploaded")

	return baseDesc, nil
}

func (e *overlaybdBuilderEngine) getLayerDir(idx int) string {
	return path.Join(e.workDir, fmt.Sprintf("%04d_", idx)+e.manifest.Layers[idx].Digest.String())
}

func (e *overlaybdBuilderEngine) create(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-create")
	dataPath := path.Join(dir, "writable_data")
	indexPath := path.Join(dir, "writable_index")
	os.RemoveAll(dataPath)
	os.RemoveAll(indexPath)
	out, err := exec.CommandContext(ctx, binpath, "-s",
		dataPath, indexPath, "64").CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-create: %s", out)
	}
	return nil
}

func (e *overlaybdBuilderEngine) apply(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-apply")

	out, err := exec.CommandContext(ctx, binpath,
		path.Join(dir, "layer.tar"),
		path.Join(dir, "config.json"),
	).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-apply: %s", out)
	}
	return nil
}

func (e *overlaybdBuilderEngine) commit(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-commit")

	out, err := exec.CommandContext(ctx, binpath, "-z", "-t",
		path.Join(dir, "writable_data"),
		path.Join(dir, "writable_index"),
		path.Join(dir, commitFile),
	).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to overlaybd-commit: %s", out)
	}
	return nil
}

type writeCountWrapper struct {
	w io.Writer
	c int64
}

func (wc *writeCountWrapper) Write(p []byte) (n int, err error) {
	n, err = wc.w.Write(p)
	wc.c += int64(n)
	return
}
