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
	"path"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/accelerated-container-image/pkg/snapshot"
	"github.com/containerd/accelerated-container-image/pkg/utils"
	"github.com/containerd/accelerated-container-image/pkg/version"
	"github.com/containerd/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (
	overlaybdBaseLayer      = "/opt/overlaybd/baselayers/ext4_64"
	commitFile              = "overlaybd.commit"
	labelDistributionSource = "containerd.io/distribution.source"
)

type overlaybdConvertResult struct {
	desc    specs.Descriptor
	chainID string
}

type overlaybdBuilderEngine struct {
	*builderEngineBase
	overlaybdConfig *snapshot.OverlayBDBSConfig
	overlaybdLayers []overlaybdConvertResult
}

func NewOverlayBDBuilderEngine(base *builderEngineBase) builderEngine {
	config := &snapshot.OverlayBDBSConfig{
		Lowers:     []snapshot.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	config.Lowers = append(config.Lowers, snapshot.OverlayBDBSConfigLower{
		File: overlaybdBaseLayer,
	})

	overlaybdLayers := make([]overlaybdConvertResult, len(base.manifest.Layers))

	var chain []digest.Digest
	srcDiffIDs := base.config.RootFS.DiffIDs

	for i := 0; i < len(base.manifest.Layers); i++ {
		chain = append(chain, srcDiffIDs[i])
		chainID := identity.ChainID(chain).String()
		overlaybdLayers[i].chainID = chainID
	}

	return &overlaybdBuilderEngine{
		builderEngineBase: base,
		overlaybdConfig:   config,
		overlaybdLayers:   overlaybdLayers,
	}
}

func (e *overlaybdBuilderEngine) DownloadLayer(ctx context.Context, idx int) error {
	desc := e.manifest.Layers[idx]
	targetFile := path.Join(e.getLayerDir(idx), "layer.tar")
	return downloadLayer(ctx, e.fetcher, targetFile, desc, true)
}

func (e *overlaybdBuilderEngine) BuildLayer(ctx context.Context, idx int) error {
	layerDir := e.getLayerDir(idx)

	alreadyConverted := false
	// check if we used a previously converted layer to skip conversion
	if _, err := os.Stat(path.Join(layerDir, commitFile)); err == nil {
		alreadyConverted = true
	}
	if !alreadyConverted {
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
		if err := e.commit(ctx, layerDir, idx); err != nil {
			return err
		}
		os.Remove(path.Join(layerDir, "layer.tar"))
		os.Remove(path.Join(layerDir, "writable_data"))
		os.Remove(path.Join(layerDir, "writable_index"))
	}
	e.overlaybdConfig.Lowers = append(e.overlaybdConfig.Lowers, snapshot.OverlayBDBSConfigLower{
		File: path.Join(layerDir, commitFile),
	})
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
		label.OverlayBDVersion:    version.OverlayBDVersionNumber,
		label.OverlayBDBlobDigest: desc.Digest.String(),
		label.OverlayBDBlobSize:   fmt.Sprintf("%d", desc.Size),
	}
	if err := uploadBlob(ctx, e.pusher, path.Join(layerDir, commitFile), desc); err != nil {
		return errors.Wrapf(err, "failed to upload layer %d", idx)
	}
	e.overlaybdLayers[idx].desc = desc
	return nil
}

func (e *overlaybdBuilderEngine) UploadImage(ctx context.Context) error {
	for idx := range e.manifest.Layers {
		e.manifest.Layers[idx] = e.overlaybdLayers[idx].desc
		e.config.RootFS.DiffIDs[idx] = e.overlaybdLayers[idx].desc.Digest
	}
	baseDesc, err := e.uploadBaseLayer(ctx)
	if err != nil {
		return err
	}
	e.manifest.Layers = append([]specs.Descriptor{baseDesc}, e.manifest.Layers...)
	e.config.RootFS.DiffIDs = append([]digest.Digest{baseDesc.Digest}, e.config.RootFS.DiffIDs...)
	return e.uploadManifestAndConfig(ctx)
}

func (e *overlaybdBuilderEngine) CheckForConvertedLayer(ctx context.Context, idx int) (specs.Descriptor, error) {
	if e.db == nil {
		return specs.Descriptor{}, errdefs.ErrNotFound
	}
	chainID := e.overlaybdLayers[idx].chainID

	// try to find in the same repo then check existence on registry
	entry := e.db.GetEntryForRepo(ctx, e.host, e.repository, chainID)
	if entry != nil && entry.ChainID != "" {
		desc := specs.Descriptor{
			MediaType: e.mediaTypeImageLayer(),
			Digest:    entry.ConvertedDigest,
			Size:      entry.DataSize,
		}
		rc, err := e.fetcher.Fetch(ctx, desc)

		if err == nil {
			rc.Close()
			logrus.Infof("layer %d found in remote with chainID %s", idx, chainID)
			return desc, nil
		}
		if errdefs.IsNotFound(err) {
			// invalid record in db, which is not found in registry, remove it
			err := e.db.DeleteEntry(ctx, e.host, e.repository, chainID)
			if err != nil {
				return specs.Descriptor{}, err
			}
		}
	}

	// found record in other repos, try mounting it to the target repo
	entries := e.db.GetCrossRepoEntries(ctx, e.host, chainID)
	for _, entry := range entries {
		desc := specs.Descriptor{
			MediaType: e.mediaTypeImageLayer(),
			Digest:    entry.ConvertedDigest,
			Size:      entry.DataSize,
			Annotations: map[string]string{
				fmt.Sprintf("%s.%s", labelDistributionSource, e.host): entry.Repository,
			},
		}

		_, err := e.pusher.Push(ctx, desc)
		if errdefs.IsAlreadyExists(err) {
			desc.Annotations = nil

			if err := e.db.CreateEntry(ctx, e.host, e.repository, entry.ConvertedDigest, chainID, entry.DataSize); err != nil {
				continue // try a different repo if available
			}

			logrus.Infof("layer %d mount from %s was successful", idx, entry.Repository)
			logrus.Infof("layer %d found in remote with chainID %s", idx, chainID)
			return desc, nil
		}
	}

	logrus.Infof("layer %d not found in remote", idx)
	return specs.Descriptor{}, errdefs.ErrNotFound
}

func (e *overlaybdBuilderEngine) StoreConvertedLayerDetails(ctx context.Context, idx int) error {
	if e.db == nil {
		return nil
	}
	return e.db.CreateEntry(ctx, e.host, e.repository, e.overlaybdLayers[idx].desc.Digest, e.overlaybdLayers[idx].chainID, e.overlaybdLayers[idx].desc.Size)
}

func (e *overlaybdBuilderEngine) DownloadConvertedLayer(ctx context.Context, idx int, desc specs.Descriptor) error {
	targetFile := path.Join(e.getLayerDir(idx), commitFile)
	return downloadLayer(ctx, e.fetcher, targetFile, desc, true)
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
			label.OverlayBDVersion:    version.OverlayBDVersionNumber,
			label.OverlayBDBlobDigest: digester.Digest().String(),
			label.OverlayBDBlobSize:   fmt.Sprintf("%d", countWriter.c),
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
	return utils.Create(ctx, dir, "-s", "64")
}

func (e *overlaybdBuilderEngine) apply(ctx context.Context, dir string) error {
	return utils.ApplyOverlaybd(ctx, dir)
}

func (e *overlaybdBuilderEngine) commit(ctx context.Context, dir string, idx int) error {
	var parentUUID string
	if idx > 0 {
		parentUUID = chainIDtoUUID(e.overlaybdLayers[idx-1].chainID)
	} else {
		parentUUID = ""
	}
	curUUID := chainIDtoUUID(e.overlaybdLayers[idx].chainID)

	if err := utils.Commit(ctx, dir, dir, false, "-z", "-t", "--uuid", curUUID); err != nil {
		return err
	}
	logrus.Infof("layer %d committed, uuid: %s, parent uuid: %s", idx, curUUID, parentUUID)
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

// UUID: 8-4-4-4-12
func chainIDtoUUID(chainID string) string {
	dStr := chainID[7:]
	return fmt.Sprintf("%s-%s-%s-%s-%s", dStr[0:8], dStr[8:12], dStr[12:16], dStr[16:20], dStr[20:32])
}
