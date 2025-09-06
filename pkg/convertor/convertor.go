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

package convertor

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/accelerated-container-image/pkg/label"
	"github.com/containerd/accelerated-container-image/pkg/utils"
	"github.com/containerd/accelerated-container-image/pkg/version"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/images/converter"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/archive"
	"github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/containerd/containerd/v2/pkg/reference"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

const (
	labelBuildLayerFrom     = "containerd.io/snapshot/overlaybd/build.layer-from"
	labelDistributionSource = "containerd.io/distribution.source"
)

var (
	emptyString string
	emptyDesc   ocispec.Descriptor
	emptyLayer  Layer

	convSnapshotNameFormat = "overlaybd-conv-%s"
	ConvContentNameFormat  = convSnapshotNameFormat
)

type ZFileConfig struct {
	Algorithm string `json:"algorithm"`
	BlockSize int    `json:"blockSize"`
}

type ImageConvertor interface {
	Convert(ctx context.Context, srcManifest ocispec.Manifest, fsType string) (ocispec.Descriptor, error)
}

type Layer struct {
	Desc   ocispec.Descriptor
	DiffID digest.Digest
}

func (l *Layer) GetInfo() (ocispec.Descriptor, digest.Digest) {
	return l.Desc, l.DiffID
}

// contentLoader can load multiple files into content.Store service, and return an oci.v1.tar layer.
func NewContentLoaderWithFsType(isAccelLayer bool, fsType string, files ...ContentFile) *contentLoader {
	return &contentLoader{
		files:        files,
		isAccelLayer: isAccelLayer,
		fsType:       fsType,
	}
}

type ContentFile struct {
	SrcFilePath string // .../{ID}/fs/overlaybd.sealed
	DstFileName string // overlaybd.commit
}

type contentLoader struct {
	files        []ContentFile
	isAccelLayer bool
	fsType       string
}

func (loader *contentLoader) Load(ctx context.Context, cs content.Store) (l Layer, err error) {
	refName := fmt.Sprintf(ConvContentNameFormat, UniquePart())
	contentWriter, err := content.OpenWriter(ctx, cs, content.WithRef(refName))
	if err != nil {
		return emptyLayer, fmt.Errorf("failed to open content writer: %w", err)
	}
	defer contentWriter.Close()

	srcPathList := make([]string, 0)
	digester := digest.Canonical.Digester()
	countWriter := &writeCountWrapper{w: io.MultiWriter(contentWriter, digester.Hash())}
	tarWriter := tar.NewWriter(countWriter)

	openedSrcFile := make([]*os.File, 0)
	defer func() {
		for _, each := range openedSrcFile {
			_ = each.Close()
		}
	}()

	for _, loader := range loader.files {
		if loader.DstFileName == "overlaybd.commit" {
			commitPath := filepath.Dir(loader.SrcFilePath)
			commitFile := filepath.Join(commitPath, "overlaybd.commit")
			srcPathList = append(srcPathList, commitFile)

			err := utils.Commit(ctx, commitPath, commitPath, true, "-z", "-t")
			if err != nil {
				return emptyLayer, fmt.Errorf("failed to overlaybd-commit for sealed file: %w", err)
			}

			srcFile, err := os.Open(commitFile)
			if err != nil {
				return emptyLayer, fmt.Errorf("failed to open src file of %s: %w", loader.SrcFilePath, err)
			}
			openedSrcFile = append(openedSrcFile, srcFile)
			_, err = io.Copy(countWriter, bufio.NewReader(srcFile))
			if err != nil {
				log.G(ctx).Errorf("failed to do io.Copy(), error: %v", err)
				return emptyLayer, err
			}
		} else {
			// normal file
			srcPathList = append(srcPathList, loader.SrcFilePath)
			srcFile, err := os.Open(loader.SrcFilePath)
			if err != nil {
				return emptyLayer, fmt.Errorf("failed to open src file of %s: %w", loader.SrcFilePath, err)
			}
			openedSrcFile = append(openedSrcFile, srcFile)

			fi, err := srcFile.Stat()
			if err != nil {
				return emptyLayer, fmt.Errorf("failed to get info of %s: %w", loader.SrcFilePath, err)
			}

			if err := tarWriter.WriteHeader(&tar.Header{
				Name:     loader.DstFileName,
				Mode:     0444,
				Size:     fi.Size(),
				Typeflag: tar.TypeReg,
			}); err != nil {
				return emptyLayer, fmt.Errorf("failed to write tar header: %w", err)
			}

			if _, err := io.Copy(tarWriter, bufio.NewReader(srcFile)); err != nil {
				return emptyLayer, fmt.Errorf("failed to copy IO: %w", err)
			}
		}
	}

	if err = tarWriter.Close(); err != nil {
		return emptyLayer, fmt.Errorf("failed to close tar file: %w", err)
	}

	labels := map[string]string{
		labelBuildLayerFrom: strings.Join(srcPathList, ","),
	}

	if err := contentWriter.Commit(ctx, countWriter.c, digester.Digest(), content.WithLabels(labels)); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return emptyLayer, fmt.Errorf("failed to commit content: %w", err)
		}
	}

	l = Layer{
		Desc: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    digester.Digest(),
			Size:      countWriter.c,
			Annotations: map[string]string{
				label.OverlayBDVersion:    version.OverlayBDVersionNumber,
				label.OverlayBDBlobDigest: digester.Digest().String(),
				label.OverlayBDBlobSize:   fmt.Sprintf("%d", countWriter.c),
			},
		},
		DiffID: digester.Digest(),
	}
	if loader.isAccelLayer {
		l.Desc.Annotations[label.AccelerationLayer] = "yes"
	}
	if loader.fsType != "" {
		l.Desc.Annotations[label.OverlayBDBlobFsType] = loader.fsType
	}
	return l, nil
}

type overlaybdConvertor struct {
	ImageConvertor
	cs       content.Store
	sn       snapshots.Snapshotter
	remote   bool
	fetcher  remotes.Fetcher
	pusher   remotes.Pusher
	db       *sql.DB
	host     string
	repo     string
	zfileCfg ZFileConfig
	vsize    int
}

func NewOverlaybdConvertor(ctx context.Context, cs content.Store, sn snapshots.Snapshotter, resolver remotes.Resolver, ref string, dbstr string, zfileCfg ZFileConfig, vsize int) (ImageConvertor, error) {
	c := &overlaybdConvertor{
		cs:       cs,
		sn:       sn,
		remote:   false,
		zfileCfg: zfileCfg,
		vsize:    vsize,
	}
	var err error
	if dbstr != "" {
		c.remote = true
		c.db, err = sql.Open("mysql", dbstr)
		if err != nil {
			return nil, err
		}
		c.pusher, err = resolver.Pusher(ctx, ref)
		if err != nil {
			return nil, err
		}
		c.fetcher, err = resolver.Fetcher(ctx, ref)
		if err != nil {
			return nil, err
		}
		refspec, err := reference.Parse(ref)
		if err != nil {
			return nil, err
		}
		c.host = refspec.Hostname()
		c.repo = strings.TrimPrefix(refspec.Locator, c.host+"/")
	}
	return c, nil
}

func (c *overlaybdConvertor) Convert(ctx context.Context, srcManifest ocispec.Manifest, fsType string) (ocispec.Descriptor, error) {
	configData, err := content.ReadBlob(ctx, c.cs, srcManifest.Config)
	if err != nil {
		return emptyDesc, err
	}

	var srcCfg ocispec.Image
	if err := json.Unmarshal(configData, &srcCfg); err != nil {
		return emptyDesc, err
	}

	committedLayers, err := c.convertLayers(ctx, srcManifest.Layers, srcCfg.RootFS.DiffIDs, fsType)
	if err != nil {
		return emptyDesc, err
	}

	return c.commitImage(ctx, srcManifest, srcCfg, committedLayers)
}

func (c *overlaybdConvertor) commitImage(ctx context.Context, srcManifest ocispec.Manifest, imgCfg ocispec.Image, committedLayers []Layer) (ocispec.Descriptor, error) {
	var copyManifest = struct {
		ocispec.Manifest `json:",omitempty"`
		// MediaType is the media type of the object this schema refers to.
		MediaType string `json:"mediaType,omitempty"`
	}{
		Manifest:  srcManifest,
		MediaType: images.MediaTypeDockerSchema2Manifest,
	}

	imgCfg.RootFS.DiffIDs = nil
	copyManifest.Layers = nil

	for _, l := range committedLayers {
		copyManifest.Layers = append(copyManifest.Layers, l.Desc)
		imgCfg.RootFS.DiffIDs = append(imgCfg.RootFS.DiffIDs, l.DiffID)
	}

	configData, err := json.MarshalIndent(imgCfg, "", "   ")
	if err != nil {
		return emptyDesc, fmt.Errorf("failed to marshal image: %w", err)
	}

	config := ocispec.Descriptor{
		MediaType: srcManifest.Config.MediaType,
		Digest:    digest.Canonical.FromBytes(configData),
		Size:      int64(len(configData)),
	}

	ref := remotes.MakeRefKey(ctx, config)
	if err := content.WriteBlob(ctx, c.cs, ref, bytes.NewReader(configData), config); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to write image config: %w", err)
	}
	if c.remote {
		if err := c.pushObject(ctx, config); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to push image config: %w", err)
		}
		log.G(ctx).Infof("config pushed")
	}

	copyManifest.Manifest.Config = config
	mb, err := json.MarshalIndent(copyManifest, "", "   ")
	if err != nil {
		return emptyDesc, err
	}

	desc := ocispec.Descriptor{
		MediaType: copyManifest.MediaType,
		Digest:    digest.Canonical.FromBytes(mb),
		Size:      int64(len(mb)),
	}

	labels := map[string]string{}
	labels["containerd.io/gc.ref.content.config"] = copyManifest.Config.Digest.String()
	for i, ch := range copyManifest.Layers {
		labels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = ch.Digest.String()
	}

	ref = remotes.MakeRefKey(ctx, desc)
	if err := content.WriteBlob(ctx, c.cs, ref, bytes.NewReader(mb), desc, content.WithLabels(labels)); err != nil {
		return emptyDesc, fmt.Errorf("failed to write image manifest: %w", err)
	}
	if c.remote {
		if err := c.pushObject(ctx, desc); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to push image manifest: %w", err)
		}
		log.G(ctx).Infof("image pushed")
	}
	return desc, nil
}

type OverlaybdLayer struct {
	Host       string
	Repo       string
	ChainID    string
	DataDigest string
	DataSize   int64
}

func (c *overlaybdConvertor) findRemote(ctx context.Context, chainID string) (ocispec.Descriptor, error) {
	row := c.db.QueryRow("select host, repo, chain_id, data_digest, data_size from overlaybd_layers where host=? and repo=? and chain_id=?", c.host, c.repo, chainID)
	// try to find in the same repo, check existence on registry
	var layer OverlaybdLayer
	if err := row.Scan(&layer.Host, &layer.Repo, &layer.ChainID, &layer.DataDigest, &layer.DataSize); err == nil {
		desc := ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    digest.Digest(layer.DataDigest),
			Size:      layer.DataSize,
		}
		rc, err := c.fetcher.Fetch(ctx, desc)
		if err == nil {
			rc.Close()
			log.G(ctx).Infof("found remote layer for chainID %s", chainID)
			return desc, nil
		}
		if errdefs.IsNotFound(err) {
			// invalid record in db, which is not found in registry, remove it
			_, err := c.db.Exec("delete from overlaybd_layers where host=? and repo=? and chain_id=?", c.host, c.repo, chainID)
			if err != nil {
				return emptyDesc, fmt.Errorf("failed to remove invalid record in db: %w", err)
			}
		}
	}

	// found record in other repo, mount it to target repo
	rows, err := c.db.Query("select host, repo, chain_id, data_digest, data_size from overlaybd_layers where host=? and chain_id=?", c.host, chainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return emptyDesc, errdefs.ErrNotFound
		}
		log.G(ctx).Infof("query error %v", err)
		return emptyDesc, err
	}
	for rows.Next() {
		var layer OverlaybdLayer
		err = rows.Scan(&layer.Host, &layer.Repo, &layer.ChainID, &layer.DataDigest, &layer.DataSize)
		if err != nil {
			continue
		}
		// try mount
		desc := ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    digest.Digest(layer.DataDigest),
			Size:      layer.DataSize,
			Annotations: map[string]string{
				fmt.Sprintf("%s.%s", labelDistributionSource, c.host): layer.Repo,
			},
		}
		_, err := c.pusher.Push(ctx, desc)
		if errdefs.IsAlreadyExists(err) {
			desc.Annotations = nil
			_, err := c.db.Exec("insert into overlaybd_layers(host, repo, chain_id, data_digest, data_size) values(?, ?, ?, ?, ?)", c.host, c.repo, chainID, desc.Digest.String(), desc.Size)
			if err != nil {
				continue
			}
			log.G(ctx).Infof("mount from %s success", layer.Repo)
			log.G(ctx).Infof("found remote layer for chainID %s", chainID)
			return desc, nil
		}
	}
	log.G(ctx).Infof("layer not found in remote")
	return emptyDesc, errdefs.ErrNotFound
}

func (c *overlaybdConvertor) pushObject(ctx context.Context, desc ocispec.Descriptor) error {
	ra, err := c.cs.ReaderAt(ctx, desc)
	if err != nil {
		return err
	}
	defer ra.Close()

	cw, err := c.pusher.Push(ctx, desc)
	if err != nil {
		if errdefs.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return content.Copy(ctx, cw, content.NewReader(ra), desc.Size, desc.Digest)
}

func (c *overlaybdConvertor) sentToRemote(ctx context.Context, desc ocispec.Descriptor, chainID string) error {
	// upload to registry
	err := c.pushObject(ctx, desc)
	if err != nil {
		return err
	}
	// update db
	_, err = c.db.Exec("insert into overlaybd_layers(host, repo, chain_id, data_digest, data_size) values(?, ?, ?, ?, ?)", c.host, c.repo, chainID, desc.Digest.String(), desc.Size)
	if err != nil {
		log.G(ctx).Warnf("failed to insert to db, err: %v", err)
		if strings.Contains(err.Error(), "Duplicate entry") {
			fmt.Printf("Conflict when inserting into db, maybe other process is converting the same blob, please try again later\n")
		}
		return err
	}
	return nil
}

// convertLayers applys image layers on overlaybd with specified filesystem and
// exports the layers based on zfile.
func (c *overlaybdConvertor) convertLayers(ctx context.Context, srcDescs []ocispec.Descriptor, srcDiffIDs []digest.Digest, fsType string) ([]Layer, error) {
	var (
		lastParentID string = ""
		err          error
		commitLayers = make([]Layer, len(srcDescs))
		chain        []digest.Digest
	)

	var sendToContentStore = func(ctx context.Context, snID string) (Layer, error) {
		info, err := c.sn.Stat(ctx, snID)
		if err != nil {
			return emptyLayer, err
		}

		loader := NewContentLoaderWithFsType(false, fsType, ContentFile{
			info.Labels[label.LocalOverlayBDPath],
			"overlaybd.commit"})
		return loader.Load(ctx, c.cs)
	}

	eg, ctx := errgroup.WithContext(ctx)
	for idx, desc := range srcDescs {
		chain = append(chain, srcDiffIDs[idx])
		chainID := identity.ChainID(chain).String()

		var remoteDesc ocispec.Descriptor

		if c.remote {
			remoteDesc, err = c.findRemote(ctx, chainID)
			if err != nil {
				if !errdefs.IsNotFound(err) {
					return nil, err
				}
			}
		}

		if c.remote && err == nil {
			key := fmt.Sprintf(convSnapshotNameFormat, chainID)
			opts := []snapshots.Opt{
				snapshots.WithLabels(map[string]string{
					"containerd.io/snapshot.ref":       key,
					"containerd.io/snapshot/image-ref": c.host + "/" + c.repo,
					label.OverlayBDBlobDigest:          remoteDesc.Digest.String(),
					label.OverlayBDBlobSize:            fmt.Sprintf("%d", remoteDesc.Size),
				}),
			}
			_, err = c.sn.Prepare(ctx, "prepare-"+key, lastParentID, opts...)
			if !errdefs.IsAlreadyExists(err) {
				// failed to prepare remote snapshot
				if err == nil {
					// rollback
					c.sn.Remove(ctx, "prepare-"+key)
				}
				return nil, fmt.Errorf("failed to prepare remote snapshot: %w", err)
			}
			lastParentID = key
			commitLayers[idx] = Layer{
				Desc: ocispec.Descriptor{
					MediaType: ocispec.MediaTypeImageLayer,
					Digest:    remoteDesc.Digest,
					Size:      remoteDesc.Size,
					Annotations: map[string]string{
						label.OverlayBDBlobDigest: remoteDesc.Digest.String(),
						label.OverlayBDBlobSize:   fmt.Sprintf("%d", remoteDesc.Size),
					},
				},
				DiffID: remoteDesc.Digest,
			}
			continue
		}

		opts := []snapshots.Opt{
			snapshots.WithLabels(map[string]string{
				label.SupportReadWriteMode: "dir",
				label.OverlayBDBlobFsType:  fsType,
				label.OverlayBDVsize:       fmt.Sprintf("%d", c.vsize),
			}),
		}
		cfgStr, err := json.Marshal(c.zfileCfg)
		if err != nil {
			return nil, err
		}
		opts = append(opts, snapshots.WithLabels(map[string]string{
			label.ZFileConfig: string(cfgStr),
		}))
		lastParentID, err = c.applyOCIV1LayerInObd(ctx, lastParentID, desc, opts, nil)
		if err != nil {
			return nil, err
		}

		if c.remote {
			// must synchronize registry and db, can not do concurrently
			commitLayers[idx], err = sendToContentStore(ctx, lastParentID)
			if err != nil {
				return nil, err
			}
			err = c.sentToRemote(ctx, commitLayers[idx].Desc, chainID)
			if err != nil {
				return nil, err
			}
		} else {
			idxI := idx
			snID := lastParentID
			eg.Go(func() error {
				var err error
				commitLayers[idxI], err = sendToContentStore(ctx, snID)
				return err
			})
		}
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return commitLayers, nil
}

// applyOCIV1LayerInObd applys the OCIv1 tarfile in overlaybd format and commit it.
func (c *overlaybdConvertor) applyOCIV1LayerInObd(
	ctx context.Context,
	parentID string, // the ID of parent snapshot
	desc ocispec.Descriptor, // the descriptor of layer
	snOpts []snapshots.Opt, // apply for the commit snapshotter
	afterApply func(root string) error, // do something after apply tar stream
) (string, error) {

	ra, err := c.cs.ReaderAt(ctx, desc)
	if err != nil {
		return emptyString, fmt.Errorf("failed to get reader %s from content store: %w", desc.Digest, err)
	}
	defer ra.Close()

	var (
		key    string
		mounts []mount.Mount
	)

	for {
		key = fmt.Sprintf(convSnapshotNameFormat, UniquePart())
		mounts, err = c.sn.Prepare(ctx, key, parentID, snOpts...)
		if err != nil {
			// retry other key
			if errdefs.IsAlreadyExists(err) {
				continue
			}
			return emptyString, fmt.Errorf("failed to preprare snapshot %q: %w", key, err)
		}

		break
	}

	var (
		rollback = true
		digester = digest.Canonical.Digester()
		rc       = io.TeeReader(content.NewReader(ra), digester.Hash())
	)

	defer func() {
		if rollback {
			if rerr := c.sn.Remove(ctx, key); rerr != nil {
				log.G(ctx).WithError(rerr).WithField("key", key).Warnf("apply failure and failed to cleanup snapshot")
			}
		}
	}()

	rc, err = compression.DecompressStream(rc)
	if err != nil {
		return emptyString, fmt.Errorf("failed to detect layer mediatype: %w", err)
	}

	if err = mount.WithTempMount(ctx, mounts, func(root string) error {
		_, err := archive.Apply(ctx, root, rc)
		if err == nil && afterApply != nil {
			err = afterApply(root)
		}
		return err
	}); err != nil {
		return emptyString, fmt.Errorf("failed to apply layer in snapshot %s: %w", key, err)
	}

	// Read any trailing data
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return emptyString, err
	}

	commitID := fmt.Sprintf(convSnapshotNameFormat, digester.Digest())
	if err = c.sn.Commit(ctx, commitID, key, snOpts...); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return emptyString, err
		}
	}

	rollback = err != nil
	return commitID, nil
}

// NOTE: based on https://github.com/containerd/containerd/blob/v1.4.3/rootfs/apply.go#L181-L187
func UniquePart() string {
	t := time.Now()
	var b [3]byte
	// Ignore read failures, just decreases uniqueness
	rand.Read(b[:])
	return fmt.Sprintf("%d-%s", t.Nanosecond(), strings.Replace(base64.URLEncoding.EncodeToString(b[:]), "_", "-", -1))
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

// NOTE: based on https://github.com/containerd/containerd/blob/v1.6.8/images/converter/converter.go#L29-L71
type options struct {
	fsType    string
	dbstr     string
	imgRef    string
	algorithm string
	blockSize int
	vsize     int
	resolver  remotes.Resolver
	client    *containerd.Client
}

type Option func(o *options) error

func WithFsType(fsType string) Option {
	return func(o *options) error {
		o.fsType = fsType
		return nil
	}
}

func WithDbstr(dbstr string) Option {
	return func(o *options) error {
		o.dbstr = dbstr
		return nil
	}
}

func WithImageRef(imgRef string) Option {
	return func(o *options) error {
		o.imgRef = imgRef
		return nil
	}
}

func WithAlgorithm(algorithm string) Option {
	return func(o *options) error {
		o.algorithm = algorithm
		return nil
	}
}

func WithBlockSize(blockSize int) Option {
	return func(o *options) error {
		o.blockSize = blockSize
		return nil
	}
}

func WithVsize(vsize int) Option {
	return func(o *options) error {
		o.vsize = vsize
		return nil
	}
}

func WithResolver(resolver remotes.Resolver) Option {
	return func(o *options) error {
		o.resolver = resolver
		return nil
	}
}

func WithClient(client *containerd.Client) Option {
	return func(o *options) error {
		o.client = client
		return nil
	}
}

func IndexConvertFunc(opts ...Option) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		var copts options
		for _, o := range opts {
			if err := o(&copts); err != nil {
				return nil, err
			}
		}
		client := copts.client
		imgRef := copts.imgRef
		sn := client.SnapshotService("overlaybd")

		srcImg, err := client.GetImage(ctx, imgRef)
		if err != nil {
			return nil, err
		}

		srcManifest, err := images.Manifest(ctx, cs, srcImg.Target(), platforms.Default())
		if err != nil {
			return nil, fmt.Errorf("failed to read manifest: %w", err)
		}
		zfileCfg := ZFileConfig{
			Algorithm: copts.algorithm,
			BlockSize: copts.blockSize,
		}
		c, err := NewOverlaybdConvertor(ctx, cs, sn, copts.resolver, imgRef, copts.dbstr, zfileCfg, copts.vsize)
		if err != nil {
			return nil, err
		}
		newMfstDesc, err := c.Convert(ctx, srcManifest, copts.fsType)
		if err != nil {
			return nil, err
		}
		return &newMfstDesc, nil
	}
}
