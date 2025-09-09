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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/containerd/accelerated-container-image/pkg/label"
	sn "github.com/containerd/accelerated-container-image/pkg/types"
	"github.com/containerd/accelerated-container-image/pkg/utils"
	"github.com/containerd/accelerated-container-image/pkg/version"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
)

const (
	overlaybdBaseLayer      = "/opt/overlaybd/baselayers/ext4_64"
	commitFile              = "overlaybd.commit"
	labelDistributionSource = "containerd.io/distribution.source"
)

type overlaybdConvertResult struct {
	desc      specs.Descriptor
	chainID   string
	fromDedup bool
}

type overlaybdBuilderEngine struct {
	*builderEngineBase
	disableSparse   bool
	overlaybdConfig *sn.OverlayBDBSConfig
	overlaybdLayers []overlaybdConvertResult
}

func NewOverlayBDBuilderEngine(base *builderEngineBase) builderEngine {
	config := &sn.OverlayBDBSConfig{
		Lowers:     []sn.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	if !base.mkfs {
		config.Lowers = append(config.Lowers, sn.OverlayBDBSConfigLower{
			File: overlaybdBaseLayer,
		})
		logrus.Infof("using default baselayer")
	}

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

	// If the layer is from dedup we should have a downloaded commit file
	commitFilePresent := false
	if _, err := os.Stat(path.Join(layerDir, commitFile)); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to check if layer %d is already present abort conversion: %w", idx, err)
		}
		// commit file is not present
	} else {
		commitFilePresent = true
		logrus.Debugf("layer %d commit file detected", idx)
	}
	if e.overlaybdLayers[idx].fromDedup {
		// check if the previously converted layer is present
		if commitFilePresent {
			logrus.Debugf("layer %d is from dedup", idx)
		} else {
			return fmt.Errorf("layer %d is from dedup but commit file is missing", idx)
		}
	} else {
		// This should not happen, but if it does, we should fail the conversion or
		// risk corrupting the image.
		if commitFilePresent {
			return fmt.Errorf("layer %d is not from dedup but commit file is present", idx)
		}

		mkfs := e.mkfs && (idx == 0)
		vsizeGB := 0
		if idx == 0 {
			if mkfs {
				vsizeGB = e.vsize
			} else {
				vsizeGB = 64 // in case that using default baselayer
			}
		}
		if err := e.create(ctx, layerDir, mkfs, vsizeGB); err != nil {
			return err
		}
		e.overlaybdConfig.Upper = sn.OverlayBDBSConfigUpper{
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
		if !e.reserve {
			os.Remove(path.Join(layerDir, "layer.tar"))
			os.Remove(path.Join(layerDir, "writable_data"))
			os.Remove(path.Join(layerDir, "writable_index"))
		}
	}
	e.overlaybdConfig.Lowers = append(e.overlaybdConfig.Lowers, sn.OverlayBDBSConfigLower{
		File: path.Join(layerDir, commitFile),
	})
	return nil
}

func (e *overlaybdBuilderEngine) UploadLayer(ctx context.Context, idx int) error {
	layerDir := e.getLayerDir(idx)
	desc, err := getFileDesc(path.Join(layerDir, commitFile), false)
	if err != nil {
		return fmt.Errorf("failed to get descriptor for layer %d: %w", idx, err)
	}
	if e.overlaybdLayers[idx].fromDedup {
		// validate that the layer digests match if the layer is from dedup
		if desc.Digest != e.overlaybdLayers[idx].desc.Digest {
			return fmt.Errorf("layer %d digest mismatch, expected %s, got %s", idx, e.overlaybdLayers[idx].desc.Digest, desc.Digest)
		}
	}
	desc.MediaType = e.mediaTypeImageLayer()
	desc.Annotations = map[string]string{
		label.OverlayBDVersion:    version.OverlayBDVersionNumber,
		label.OverlayBDBlobDigest: desc.Digest.String(),
		label.OverlayBDBlobSize:   fmt.Sprintf("%d", desc.Size),
	}
	if !e.noUpload {
		if err := uploadBlob(ctx, e.pusher, path.Join(layerDir, commitFile), desc); err != nil {
			return fmt.Errorf("failed to upload layer %d: %w", idx, err)
		}
	}
	e.overlaybdLayers[idx].desc = desc
	return nil
}

func (e *overlaybdBuilderEngine) UploadImage(ctx context.Context) (specs.Descriptor, error) {
	for idx := range e.manifest.Layers {
		e.manifest.Layers[idx] = e.overlaybdLayers[idx].desc
		e.config.RootFS.DiffIDs[idx] = e.overlaybdLayers[idx].desc.Digest
	}
	if !e.mkfs {
		baseDesc, err := e.uploadBaseLayer(ctx)
		if err != nil {
			return specs.Descriptor{}, err
		}
		e.manifest.Layers = append([]specs.Descriptor{baseDesc}, e.manifest.Layers...)
		e.config.RootFS.DiffIDs = append([]digest.Digest{baseDesc.Digest}, e.config.RootFS.DiffIDs...)
	}
	if e.referrer {
		e.manifest.ArtifactType = ArtifactTypeOverlaybd
		e.manifest.Subject = &specs.Descriptor{
			MediaType: e.inputDesc.MediaType,
			Digest:    e.inputDesc.Digest,
			Size:      e.inputDesc.Size,
		}
	}
	return e.uploadManifestAndConfig(ctx)
}

func (e *overlaybdBuilderEngine) CheckForConvertedLayer(ctx context.Context, idx int) (specs.Descriptor, error) {
	if e.db == nil {
		return specs.Descriptor{}, errdefs.ErrNotFound
	}
	chainID := e.overlaybdLayers[idx].chainID

	// try to find the layer in the target repo
	entry := e.db.GetLayerEntryForRepo(ctx, e.host, e.repository, chainID)
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
			err := e.db.DeleteLayerEntry(ctx, e.host, e.repository, chainID)
			if err != nil {
				return specs.Descriptor{}, err
			}
		}
	}

	// fallback to a registry wide search
	// found record in other repos, try mounting it to the target repo
	entries := e.db.GetCrossRepoLayerEntries(ctx, e.host, chainID)
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

			if err := e.db.CreateLayerEntry(ctx, e.host, e.repository, entry.ConvertedDigest, chainID, entry.DataSize); err != nil {
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

// If manifest is already converted, avoid conversion. (e.g During tag reuse or cross repo mounts)
// Note: This is output mediatype sensitive, if the manifest is converted to a different mediatype,
// we will still convert it normally.
func (e *overlaybdBuilderEngine) CheckForConvertedManifest(ctx context.Context) (specs.Descriptor, error) {
	if e.db == nil {
		return specs.Descriptor{}, errdefs.ErrNotFound
	}

	// try to find the manifest in the target repo
	entry := e.db.GetManifestEntryForRepo(ctx, e.host, e.repository, e.mediaTypeManifest(), e.inputDesc.Digest)
	if entry != nil && entry.ConvertedDigest != "" {
		convertedDesc := specs.Descriptor{
			MediaType: e.mediaTypeManifest(),
			Digest:    entry.ConvertedDigest,
			Size:      entry.DataSize,
		}
		rc, err := e.fetcher.Fetch(ctx, convertedDesc)
		if err == nil {
			rc.Close()
			logrus.Infof("manifest %s found in remote with resulting digest %s", e.inputDesc.Digest, convertedDesc.Digest)
			return convertedDesc, nil
		}
		if errdefs.IsNotFound(err) {
			// invalid record in db, which is not found in registry, remove it
			err := e.db.DeleteManifestEntry(ctx, e.host, e.repository, e.mediaTypeManifest(), e.inputDesc.Digest)
			if err != nil {
				return specs.Descriptor{}, err
			}
		}
	}
	// fallback to a registry wide search
	entries := e.db.GetCrossRepoManifestEntries(ctx, e.host, e.mediaTypeManifest(), e.inputDesc.Digest)
	for _, entry := range entries {
		convertedDesc := specs.Descriptor{
			MediaType: e.mediaTypeManifest(),
			Digest:    entry.ConvertedDigest,
			Size:      entry.DataSize,
		}
		fetcher, err := e.resolver.Fetcher(ctx, fmt.Sprintf("%s/%s@%s", entry.Host, entry.Repository, convertedDesc.Digest.String()))
		if err != nil {
			return specs.Descriptor{}, err
		}
		manifest, err := fetchManifest(ctx, fetcher, convertedDesc)
		if err != nil {
			if errdefs.IsNotFound(err) {
				// invalid record in db, which is not found in registry, remove it
				err := e.db.DeleteManifestEntry(ctx, entry.Host, entry.Repository, e.mediaTypeManifest(), e.inputDesc.Digest)
				if err != nil {
					return specs.Descriptor{}, err
				}
			}
			continue
		}
		if err := e.mountImage(ctx, *manifest, convertedDesc, entry.Repository); err != nil {
			continue // try a different repo if available
		}
		if err := e.db.CreateManifestEntry(ctx, e.host, e.repository, e.mediaTypeManifest(), e.inputDesc.Digest, convertedDesc.Digest, entry.DataSize); err != nil {
			continue // try a different repo if available
		}
		logrus.Infof("manifest %s mount from %s was successful", convertedDesc.Digest, entry.Repository)
		return convertedDesc, nil
	}

	logrus.Infof("manifest %s not found already converted in remote", e.inputDesc.Digest)
	return specs.Descriptor{}, errdefs.ErrNotFound
}

// If a converted manifest has been found we still need to tag it to match the expected output tag.
func (e *overlaybdBuilderEngine) TagPreviouslyConvertedManifest(ctx context.Context, desc specs.Descriptor) error {
	return tagPreviouslyConvertedManifest(ctx, e.pusher, e.fetcher, desc)
}

// mountImage is responsible for mounting a specific manifest from a source repository, this includes
// mounting all layers + config and then pushing the manifest.
func (e *overlaybdBuilderEngine) mountImage(ctx context.Context, manifest specs.Manifest, desc specs.Descriptor, mountRepository string) error {
	// Mount Config Blobs
	config := manifest.Config
	config.Annotations = map[string]string{
		fmt.Sprintf("%s.%s", labelDistributionSource, e.host): mountRepository,
	}
	_, err := e.pusher.Push(ctx, config)
	if errdefs.IsAlreadyExists(err) {
		logrus.Infof("config blob mount from %s was successful", mountRepository)
	} else if err != nil {
		return fmt.Errorf("Failed to mount config blob from %s repository : %w", mountRepository, err)
	}

	// Mount Layer Blobs
	for idx, layer := range manifest.Layers {
		desc := layer
		desc.Annotations = map[string]string{
			fmt.Sprintf("%s.%s", labelDistributionSource, e.host): mountRepository,
		}
		_, err := e.pusher.Push(ctx, desc)
		if errdefs.IsAlreadyExists(err) {
			logrus.Infof("layer %d mount from %s was successful", idx, mountRepository)
		} else if err != nil {
			return fmt.Errorf("failed to mount all layers from %s repository : %w", mountRepository, err)
		}
	}

	// Push Manifest
	cbuf, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return uploadBytes(ctx, e.pusher, desc, cbuf)
}

func (e *overlaybdBuilderEngine) StoreConvertedManifestDetails(ctx context.Context) error {
	if e.db == nil {
		return nil
	}
	if e.outputDesc.Digest == "" {
		return errors.New("manifest is not yet converted")
	}
	return e.db.CreateManifestEntry(ctx, e.host, e.repository, e.mediaTypeManifest(), e.inputDesc.Digest, e.outputDesc.Digest, e.outputDesc.Size)
}

func (e *overlaybdBuilderEngine) StoreConvertedLayerDetails(ctx context.Context, idx int) error {
	if e.db == nil {
		return nil
	}
	if e.overlaybdLayers[idx].fromDedup {
		logrus.Infof("layer %d skip storing conversion details", idx)
		return nil
	}
	return e.db.CreateLayerEntry(ctx, e.host, e.repository, e.overlaybdLayers[idx].desc.Digest, e.overlaybdLayers[idx].chainID, e.overlaybdLayers[idx].desc.Size)
}

func (e *overlaybdBuilderEngine) DownloadConvertedLayer(ctx context.Context, idx int, desc specs.Descriptor) error {
	targetFile := path.Join(e.getLayerDir(idx), commitFile)
	err := downloadLayer(ctx, e.fetcher, targetFile, desc, true)
	if err != nil {
		// We should remove the commit file if the download failed to allow for fallback conversion
		os.Remove(targetFile) // Remove any file that may have failed to download
		return fmt.Errorf("failed to download layer %d: %w", idx, err)
	}
	// Mark that this layer is from dedup
	e.overlaybdLayers[idx].fromDedup = true
	e.overlaybdLayers[idx].desc = desc // If we are deduping store the dedup descriptor for later validation
	return nil
}

func (e *overlaybdBuilderEngine) Cleanup() {
	if !e.reserve {
		os.RemoveAll(e.workDir)
	}
}

func (e *overlaybdBuilderEngine) uploadBaseLayer(ctx context.Context) (specs.Descriptor, error) {
	// add baselayer with tar header
	tarFile := path.Join(e.workDir, "ext4_64.tar")
	fdes, err := os.Create(tarFile)
	if err != nil {
		return specs.Descriptor{}, fmt.Errorf("failed to create file %s: %w", tarFile, err)
	}
	digester := digest.Canonical.Digester()
	countWriter := &writeCountWrapper{w: io.MultiWriter(fdes, digester.Hash())}
	tarWriter := tar.NewWriter(countWriter)

	fsrc, err := os.Open(overlaybdBaseLayer)
	if err != nil {
		return specs.Descriptor{}, fmt.Errorf("failed to open %s: %w", overlaybdBaseLayer, err)
	}
	fstat, err := os.Stat(overlaybdBaseLayer)
	if err != nil {
		return specs.Descriptor{}, fmt.Errorf("failed to get info of %s: %w", overlaybdBaseLayer, err)
	}

	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     commitFile,
		Mode:     0444,
		Size:     fstat.Size(),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return specs.Descriptor{}, fmt.Errorf("failed to write tar header: %w", err)
	}
	if _, err := io.Copy(tarWriter, bufio.NewReader(fsrc)); err != nil {
		return specs.Descriptor{}, fmt.Errorf("failed to copy IO: %w", err)
	}
	if err = tarWriter.Close(); err != nil {
		return specs.Descriptor{}, fmt.Errorf("failed to close tar file: %w", err)
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
	if !e.noUpload {
		if err = uploadBlob(ctx, e.pusher, tarFile, baseDesc); err != nil {
			return specs.Descriptor{}, fmt.Errorf("failed to upload baselayer: %w", err)
		}
		logrus.Infof("baselayer uploaded")
	}
	return baseDesc, nil
}

func (e *overlaybdBuilderEngine) getLayerDir(idx int) string {
	return path.Join(e.workDir, fmt.Sprintf("%04d_", idx)+e.manifest.Layers[idx].Digest.String())
}

func (e *overlaybdBuilderEngine) create(ctx context.Context, dir string, mkfs bool, vsizeGB int) error {
	opts := []string{fmt.Sprintf("%d", vsizeGB)}
	if !e.disableSparse {
		opts = append(opts, "-s")
	}
	if mkfs {
		opts = append(opts, "--mkfs")
		logrus.Infof("mkfs for baselayer, vsize: %d GB", vsizeGB)
	}
	return utils.Create(ctx, dir, opts...)
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
	opts := []string{"-z", "-t", "--uuid", curUUID}
	if parentUUID != "" {
		opts = append(opts, "--parent-uuid", parentUUID)
	}
	if err := utils.Commit(ctx, dir, dir, false, opts...); err != nil {
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
