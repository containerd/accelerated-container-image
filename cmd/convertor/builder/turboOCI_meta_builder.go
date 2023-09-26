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
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/accelerated-container-image/pkg/snapshot"
	"github.com/containerd/accelerated-container-image/pkg/utils"
	"github.com/containerd/accelerated-container-image/pkg/version"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/log"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

// turboOCIMetaBuilderEngine use turboOCI-apply instead of overlaybd-apply
//
// Note:
//   1. turboOCIMetaBuilderEngine no longer use an additional baselayer and will
//      instead using mkfs, even if `--mkfs` is not specified

type turboOCIMetaBuilderEngine struct {
	*builderEngineBase
	overlaybdConfig *snapshot.OverlayBDBSConfig
	isGzip          []bool
	orgLayers       []specs.Descriptor
	*auditor
}

func NewTurboOCIMetaBuilderEngine(base *builderEngineBase) builderEngine {
	config := &snapshot.OverlayBDBSConfig{
		Lowers:     []snapshot.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	orgLayers := make([]specs.Descriptor, len(base.manifest.Layers))
	copy(orgLayers, base.manifest.Layers) // just shallow copy
	return &turboOCIMetaBuilderEngine{
		builderEngineBase: base,
		overlaybdConfig:   config,
		isGzip:            make([]bool, len(base.manifest.Layers)),
		orgLayers:         orgLayers,
		auditor:           newAuditor(base.auditPath),
	}
}

// Get gzip index and tar header (self-produce or fetch from remote)
func (e *turboOCIMetaBuilderEngine) DownloadLayer(ctx context.Context, idx int) error {
	var err error
	if e.isGzip[idx], err = e.isGzipLayer(ctx, idx); err != nil {
		return fmt.Errorf("turboOCIMeta: failed to check layer compression type: %w", err)
	}

	if err := e.prepareBuildFromRemote(ctx, idx); err == nil {
		return nil
	} else if err != errdefs.ErrNotFound {
		return fmt.Errorf("failed to get tar header from remote: %w", err)
	} else {
		log.G(ctx).Debug("tar header not found from remote, prepare it locally")
	}

	if err := e.prepareBuildFromLocal(ctx, idx); err != nil {
		return fmt.Errorf("failed to prepare tar header locally: %w", err)
	}
	return nil
}

func (e *turboOCIMetaBuilderEngine) BuildLayer(ctx context.Context, idx int) error {
	startTime := time.Now()
	if err := e.buildFSMeta(ctx, idx); err != nil {
		return fmt.Errorf("turboOCIMeta: failed to build %s: %w", fsMetaFile, err)
	}
	file, err := os.Create(e.pathIdentifier(idx))
	if err != nil {
		return fmt.Errorf("turboOCIMeta: failed to create identifier file: %w", err)
	}
	file.Close()

	files := []string{
		e.pathFSMeta(idx),
		e.pathIdentifier(idx),
	}
	if e.isGzip[idx] {
		files = append(files, e.pathGzipIndex(idx))
	}
	if err := buildArchiveFromFiles(ctx, e.pathTurboOCILayer(idx), compression.Gzip, files...); err != nil {
		return fmt.Errorf("turboOCIMeta: failed to tar: %w", err)
	}
	elapsed := time.Since(startTime)
	e.audit(idx, "build", elapsed.Seconds())
	return nil
}

func (e *turboOCIMetaBuilderEngine) UploadLayer(ctx context.Context, idx int) error {
	desc, err := getFileDesc(e.pathTurboOCILayer(idx), false)
	if err != nil {
		return fmt.Errorf("turboOCIMeta: failed to get descriptor: %w", err)
	}
	var targetMediaType string
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

	desc.MediaType = e.mediaTypeImageLayerGzip()
	desc.Annotations = map[string]string{
		label.OverlayBDBlobDigest: desc.Digest.String(),
		label.OverlayBDBlobSize:   fmt.Sprintf("%d", desc.Size),
		label.TurboOCIDigest:      e.manifest.Layers[idx].Digest.String(),
		label.TurboOCIMediaType:   targetMediaType,
		label.OverlayBDVersion:    version.TurboOCIVersionNumber,
	}
	startTime := time.Now()
	if err := uploadBlob(ctx, e.pusher, e.pathTurboOCILayer(idx), desc); err != nil {
		return fmt.Errorf("turboOCIMeta: failed to upload blob: %w", err)
	}
	elapsed := time.Since(startTime)
	e.audit(idx, "upload", elapsed.Seconds())
	e.manifest.Layers[idx] = desc

	descTarHeader, _ := getFileDesc(e.pathTarHeader(idx), false)
	descGzipIndex, _ := getFileDesc(e.pathGzipIndex(idx), false)
	descFSMeta, _ := getFileDesc(e.pathFSMeta(idx), false)
	e.audit(idx, "tar header", descTarHeader.Size)
	e.audit(idx, "gzip index", descGzipIndex.Size)
	e.audit(idx, "fs meta", descFSMeta.Size)
	e.audit(idx, "turboOCI layer", desc.Size)
	e.audit(idx, "ratio", fmt.Sprintf("%.3f %%", 100.0*float64(desc.Size)/float64(e.orgLayers[idx].Size)))
	e.audit(idx, "oci layer", e.orgLayers[idx].Size)
	return nil
}

func (e *turboOCIMetaBuilderEngine) UploadImage(ctx context.Context) error {
	for idx := range e.manifest.Layers {
		desc, err := getFileDesc(e.pathTurboOCILayer(idx), true)
		if err != nil {
			return fmt.Errorf("turboOCIMeta: failed to get descriptor (uncompressed): %w", err)
		}
		e.config.RootFS.DiffIDs[idx] = desc.Digest
	}
	if err := e.uploadManifestAndConfig(ctx); err != nil {
		return fmt.Errorf("turboOCIMeta: failed to upload manifest and config: %w", err)
	}

	if e.auditPath != "" {
		if err := e.dumpAudit([]string{"oci layer", "download", "export", "build", "upload",
			"turboOCI layer", "ratio", "fs meta", "gzip index", "tar header"}); err != nil {
			return fmt.Errorf("failed to dump audit: %w", err)
		}
	}
	return nil
}

// CheckForConvertedLayer TODO
func (e *turboOCIMetaBuilderEngine) CheckForConvertedLayer(ctx context.Context, idx int) (specs.Descriptor, error) {
	return specs.Descriptor{}, errdefs.ErrNotFound
}

// StoreConvertedLayerDetails TODO
func (e *turboOCIMetaBuilderEngine) StoreConvertedLayerDetails(ctx context.Context, idx int) error {
	return nil
}

// DownloadConvertedLayer TODO
func (e *turboOCIMetaBuilderEngine) DownloadConvertedLayer(ctx context.Context, idx int, desc specs.Descriptor) error {
	return errdefs.ErrNotImplemented
}

func (e *turboOCIMetaBuilderEngine) Cleanup() {
	os.RemoveAll(e.workDir)
}

// TODO: get tar header from remote
func (e *turboOCIMetaBuilderEngine) prepareBuildFromRemote(ctx context.Context, idx int) error {
	return errdefs.ErrNotFound
}

func (e *turboOCIMetaBuilderEngine) prepareBuildFromLocal(ctx context.Context, idx int) error {
	desc := e.manifest.Layers[idx]
	startTime := time.Now()
	if err := downloadLayer(ctx, e.fetcher, e.pathLayerFile(idx), desc, false); err != nil {
		return fmt.Errorf("turboOCIMeta: failed to download layer: %w", err)
	}
	elapsed := time.Since(startTime)
	e.audit(idx, "download", elapsed.Seconds())

	startTime = time.Now()
	if err := e.export(ctx, idx); err != nil {
		return fmt.Errorf("turboOCIMeta: failed to export tar header and gzip index: %w", err)
	}
	elapsed = time.Since(startTime)
	e.audit(idx, "export", elapsed.Seconds())
	return nil
}

func (e *turboOCIMetaBuilderEngine) buildFSMeta(ctx context.Context, idx int) error {
	if err := e.create(ctx, idx); err != nil {
		return err
	}
	e.overlaybdConfig.Upper = snapshot.OverlayBDBSConfigUpper{
		Data:  e.pathWritableData(idx),
		Index: e.pathWritableIndex(idx),

		// Used as placeholder, this file is not actually read in the conversion workflow
		Target: e.pathTarHeader(idx),
	}
	if err := writeConfig(e.pathLayerDir(idx), e.overlaybdConfig); err != nil {
		return err
	}
	if err := e.apply(ctx, idx); err != nil {
		return err
	}
	if err := e.commit(ctx, idx); err != nil {
		return err
	}
	e.overlaybdConfig.Lowers = append(e.overlaybdConfig.Lowers, snapshot.OverlayBDBSConfigLower{
		File: e.pathFSMeta(idx),

		// Used as placeholder, this file is not actually read in the conversion workflow
		TargetFile: e.pathTarHeader(idx),
	})
	os.Remove(e.pathWritableData(idx))
	os.Remove(e.pathWritableIndex(idx))
	return nil
}

// -------------------- path --------------------

func (e *turboOCIMetaBuilderEngine) pathLayerDir(idx int) string {
	return path.Join(e.workDir, fmt.Sprintf("%04d_", idx)+e.orgLayers[idx].Digest.String())
}

func (e *turboOCIMetaBuilderEngine) pathLayerFile(idx int) string {
	return path.Join(e.pathLayerDir(idx), "layer.src")
}

func (e *turboOCIMetaBuilderEngine) pathTarHeader(idx int) string {
	return path.Join(e.pathLayerDir(idx), "tar.meta")
}

func (e *turboOCIMetaBuilderEngine) pathGzipIndex(idx int) string {
	return path.Join(e.pathLayerDir(idx), gzipMetaFile)
}

func (e *turboOCIMetaBuilderEngine) pathWritableData(idx int) string {
	return path.Join(e.pathLayerDir(idx), "writable_data")
}

func (e *turboOCIMetaBuilderEngine) pathWritableIndex(idx int) string {
	return path.Join(e.pathLayerDir(idx), "writable_index")
}

func (e *turboOCIMetaBuilderEngine) pathImageConfig(idx int) string {
	return path.Join(e.pathLayerDir(idx), "config.json")
}

func (e *turboOCIMetaBuilderEngine) pathFSMeta(idx int) string {
	return path.Join(e.pathLayerDir(idx), fsMetaFile)
}

func (e *turboOCIMetaBuilderEngine) pathIdentifier(idx int) string {
	return path.Join(e.pathLayerDir(idx), tociIdentifier)
}

func (e *turboOCIMetaBuilderEngine) pathTurboOCILayer(idx int) string {
	return path.Join(e.pathLayerDir(idx), tociLayerTar)
}

// -------------------- cmd --------------------

var obdBinTurboOCIApply = "/opt/overlaybd/bin/turboOCI-apply"

func (e *turboOCIMetaBuilderEngine) export(ctx context.Context, idx int) error {
	args := []string{
		"--export",
		e.pathLayerFile(idx),
		e.pathTarHeader(idx),
	}
	if e.isGzip[idx] {
		args = append(args, "--gz_index_path", e.pathGzipIndex(idx))
	}
	if out, err := exec.CommandContext(ctx, obdBinTurboOCIApply, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to turboOCI-apply [export], out: %s, err: %w", out, err)
	}
	return nil
}

func (e *turboOCIMetaBuilderEngine) create(ctx context.Context, idx int) error {
	opts := []string{"64", "--turboOCI"}
	if idx == 0 {
		opts = append(opts, "--mkfs")
	}
	return utils.Create(ctx, e.pathLayerDir(idx), opts...)
}

func (e *turboOCIMetaBuilderEngine) apply(ctx context.Context, idx int) error {
	if out, err := exec.CommandContext(ctx, obdBinTurboOCIApply,
		"--import",
		e.pathTarHeader(idx),
		e.pathImageConfig(idx),
	).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to turboOCI-apply [import], out: %s, err: %w", out, err)
	}
	return nil
}

func (e *turboOCIMetaBuilderEngine) commit(ctx context.Context, idx int) error {
	dir := e.pathLayerDir(idx)
	if err := utils.Commit(ctx, dir, dir, false, "-z", "--turboOCI"); err != nil {
		return err
	}
	if err := os.Rename(path.Join(dir, commitFile), e.pathFSMeta(idx)); err != nil {
		return fmt.Errorf("failed to rename commit file to %s: %w", fsMetaFile, err)
	}
	return nil
}

// -------------------- audit --------------------

type auditEntry struct {
	layer int
	name  string
}

type auditor struct {
	entries map[auditEntry]any
	output  string
}

func newAuditor(output string) *auditor {
	return &auditor{
		entries: make(map[auditEntry]any),
		output:  output,
	}
}

func (adt *auditor) audit(layer int, name string, value any) {
	adt.entries[auditEntry{
		layer: layer,
		name:  name,
	}] = value
}

func (adt *auditor) dumpAudit(entryNames []string) error {
	file, err := os.OpenFile(adt.output, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open dump target file: %w", err)
	}
	defer file.Close()
	w := csv.NewWriter(file)
	var headers []string
	headers = append(headers, "layer")
	headers = append(headers, entryNames...)
	if err := w.Write(headers); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}
	idx := 0
	for {
		var record []string
		for _, name := range entryNames {
			if val, ok := adt.entries[auditEntry{
				layer: idx,
				name:  name,
			}]; ok {
				record = append(record, fmt.Sprintf("%v", val))
			}
		}
		if len(record) == 0 {
			break
		}
		record = append([]string{fmt.Sprintf("%d", idx)}, record...)
		if err := w.Write(record); err != nil {
			return fmt.Errorf("failed to write record %v: %w", record, err)
		}
		idx++
	}
	w.Flush()
	return nil
}
