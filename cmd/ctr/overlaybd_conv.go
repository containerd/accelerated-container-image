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

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/archive"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/cmd/ctr/commands"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/snapshots"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"golang.org/x/sync/errgroup"
)

var (
	emptyString string
	emptyDesc   ocispec.Descriptor
	emptyLayer  layer

	convSnapshotNameFormat = "overlaybd-conv-%s"
	convLeaseNameFormat    = convSnapshotNameFormat
	convContentNameFormat  = convSnapshotNameFormat
)

var convertCommand = cli.Command{
	Name:        "obdconv",
	Usage:       "convert image layer into overlaybd format type",
	ArgsUsage:   "<src-image> <dst-image>",
	Description: `Export images to an OCI tar[.gz] into zfile format`,
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "basepath",
			Usage: "baselayer path(required), used to init block device",
			Value: "/opt/overlaybd/baselayers/ext4_64",
		},
		cli.StringFlag{
			Name:  "fstype",
			Usage: "filesystem type(required), used to mount block device, support specifying mount options and mkfs options, separate fs type and options by ';', separate mount options by ',', separate mkfs options by ' '",
			Value: "ext4",
		},
		cli.BoolFlag{
			Name:   "build-baselayer-only",
			Usage:  "build base layer only",
			Hidden: false,
		},
	},
	Action: func(context *cli.Context) error {
		var (
			srcImage    = context.Args().First()
			targetImage = context.Args().Get(1)
		)

		if (srcImage == "" || targetImage == "") && !context.Bool("build-baselayer-only") {
			return errors.New("please provide src image name(must in local) and dest image name")
		}

		cli, ctx, cancel, err := commands.NewClient(context)
		if err != nil {
			return err
		}
		defer cancel()

		ctx, done, err := cli.WithLease(ctx,
			leases.WithID(fmt.Sprintf(convLeaseNameFormat, uniquePart())),
			leases.WithExpiration(1*time.Hour),
		)
		if err != nil {
			return errors.Wrap(err, "failed to create lease")
		}
		defer done(ctx)

		var (
			sn = cli.SnapshotService("overlaybd")
			cs = cli.ContentStore()
		)

		fsType := context.String("fstype")
		fmt.Printf("file system type: %s\n", fsType)
		basePath := context.String("basepath")
		fmt.Printf("base layer path: %s\n", basePath)
		var baseLayer *layer = nil
		_, exist := os.Stat(basePath)
		if exist == nil {
			if context.Bool("build-baselayer-only") {
				fmt.Printf("build base layer only, base layer exists, build base layer failed\n")
				return nil
			}
			loader := newContentLoaderWithFsType(false, fsType, contentFile{
				context.String("basepath"), "overlaybd.commit"})
			l, err := loader.Load(ctx, cs)
			if err != nil {
				return errors.Wrap(err, "failed to load baselayer into content.Store")
			}
			baseLayer = &l
		} else {
			fmt.Printf("base layer does not exist, then build it\n")
			if context.Bool("build-baselayer-only") {
				fmt.Printf("build base layer only\n")
				_, err = buildBaseLayerInZfile(ctx, sn, fsType, basePath)
				if err == nil {
					fmt.Printf("build base layer successfully\n")
				} else {
					fmt.Printf("build base layer failed\n")
				}
				return err
			}
		}

		srcImg, err := ensureImageExist(ctx, cli, srcImage)
		if err != nil {
			return err
		}

		srcManifest, err := currentPlatformManifest(ctx, cs, srcImg)
		if err != nil {
			return errors.Wrapf(err, "failed to read manifest")
		}

		committedLayers, err := convOCIV1LayersToZfile(ctx, sn, cs, baseLayer, srcManifest.Layers, fsType, basePath)
		if err != nil {
			return err
		}

		newMfstDesc, err := commitOverlaybdImage(ctx, cs, srcManifest, committedLayers)
		if err != nil {
			return err
		}

		newImage := images.Image{
			Name:   targetImage,
			Target: newMfstDesc,
		}
		return createImage(ctx, cli.ImageService(), newImage)
	},
}

type layer struct {
	desc   ocispec.Descriptor
	diffID digest.Digest
}

// contentLoader can load multiple files into content.Store service, and return an oci.v1.tar layer.
func newContentLoader(isAccelLayer bool, files ...contentFile) *contentLoader {
	return &contentLoader{
		files:        files,
		isAccelLayer: isAccelLayer,
		fsType:       "",
	}
}

func newContentLoaderWithFsType(isAccelLayer bool, fsType string, files ...contentFile) *contentLoader {
	return &contentLoader{
		files:        files,
		isAccelLayer: isAccelLayer,
		fsType:       fsType,
	}
}

type contentFile struct {
	srcFilePath string
	dstFileName string
}

type contentLoader struct {
	files        []contentFile
	isAccelLayer bool
	fsType       string
}

func (loader *contentLoader) Load(ctx context.Context, cs content.Store) (l layer, err error) {
	const (
		annoOverlayBDBlobDigest  = "containerd.io/snapshot/overlaybd/blob-digest"
		annoOverlayBDBlobSize    = "containerd.io/snapshot/overlaybd/blob-size"
		annoOverlayBDBlobFsType  = "containerd.io/snapshot/overlaybd/blob-fs-type"
		annoKeyAccelerationLayer = "containerd.io/snapshot/overlaybd/acceleration-layer"
		labelBuildLayerFrom      = "containerd.io/snapshot/overlaybd/build.layer-from"
	)

	refName := fmt.Sprintf(convContentNameFormat, uniquePart())
	contentWriter, err := content.OpenWriter(ctx, cs, content.WithRef(refName))
	if err != nil {
		return emptyLayer, errors.Wrapf(err, "failed to open content writer")
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
		srcPathList = append(srcPathList, loader.srcFilePath)
		srcFile, err := os.Open(loader.srcFilePath)
		if err != nil {
			return emptyLayer, errors.Wrapf(err, "failed to open src file of %s", loader.srcFilePath)
		}
		openedSrcFile = append(openedSrcFile, srcFile)

		fi, err := os.Stat(loader.srcFilePath)
		if err != nil {
			return emptyLayer, errors.Wrapf(err, "failed to get info of %s", loader.srcFilePath)
		}

		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     loader.dstFileName,
			Mode:     0444,
			Size:     fi.Size(),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return emptyLayer, errors.Wrapf(err, "failed to write tar header")
		}

		if _, err := io.Copy(tarWriter, bufio.NewReader(srcFile)); err != nil {
			return emptyLayer, errors.Wrapf(err, "failed to copy IO")
		}
	}

	if err = tarWriter.Close(); err != nil {
		return emptyLayer, errors.Wrapf(err, "failed to close tar file")
	}

	labels := map[string]string{
		labelBuildLayerFrom: strings.Join(srcPathList, ","),
	}

	if err := contentWriter.Commit(ctx, countWriter.c, digester.Digest(), content.WithLabels(labels)); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return emptyLayer, errors.Wrapf(err, "failed to commit content")
		}
	}

	l = layer{
		desc: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    digester.Digest(),
			Size:      countWriter.c,
			Annotations: map[string]string{
				annoOverlayBDBlobDigest: digester.Digest().String(),
				annoOverlayBDBlobSize:   fmt.Sprintf("%d", countWriter.c),
			},
		},
		diffID: digester.Digest(),
	}
	if loader.isAccelLayer {
		l.desc.Annotations[annoKeyAccelerationLayer] = "yes"
	}
	if loader.fsType != "" {
		l.desc.Annotations[annoOverlayBDBlobFsType] = loader.fsType
	}
	return l, nil
}

func commitOverlaybdImage(ctx context.Context, cs content.Store, srcManifest ocispec.Manifest, committedLayers []layer) (_ ocispec.Descriptor, err0 error) {
	var copyManifest = struct {
		ocispec.Manifest `json:",omitempty"`
		// MediaType is the media type of the object this schema refers to.
		MediaType string `json:"mediaType,omitempty"`
	}{
		Manifest:  srcManifest,
		MediaType: images.MediaTypeDockerSchema2Manifest,
	}

	// new image config
	configData, err := content.ReadBlob(ctx, cs, copyManifest.Manifest.Config)
	if err != nil {
		return emptyDesc, err
	}

	var imgCfg ocispec.Image
	if err := json.Unmarshal(configData, &imgCfg); err != nil {
		return emptyDesc, err
	}

	srcHistory := imgCfg.History

	imgCfg.History = nil
	imgCfg.RootFS.DiffIDs = nil
	copyManifest.Layers = nil

	buildTime := time.Now()
	for idx, l := range committedLayers {
		copyManifest.Layers = append(copyManifest.Layers, l.desc)
		imgCfg.RootFS.DiffIDs = append(imgCfg.RootFS.DiffIDs, l.diffID)

		createdBy := "/bin/sh -c #(nop)  init overlaybd base layer"
		if idx != 0 {
			createdBy = srcHistory[idx-1].CreatedBy
		}

		imgCfg.History = append(imgCfg.History, ocispec.History{
			Created:   &buildTime,
			CreatedBy: createdBy,
		})
	}

	for i, j := 0, len(imgCfg.History)-1; i < j; i, j = i+1, j-1 {
		imgCfg.History[i], imgCfg.History[j] = imgCfg.History[j], imgCfg.History[i]
	}

	configData, err = json.MarshalIndent(imgCfg, "", "   ")
	if err != nil {
		return emptyDesc, errors.Wrap(err, "failed to marshal image")
	}

	config := ocispec.Descriptor{
		MediaType: srcManifest.Config.MediaType,
		Digest:    digest.Canonical.FromBytes(configData),
		Size:      int64(len(configData)),
	}

	ref := remotes.MakeRefKey(ctx, config)
	if err := content.WriteBlob(ctx, cs, ref, bytes.NewReader(configData), config); err != nil {
		return ocispec.Descriptor{}, errors.Wrap(err, "failed to write image config")
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
	if err := content.WriteBlob(ctx, cs, ref, bytes.NewReader(mb), desc, content.WithLabels(labels)); err != nil {
		return emptyDesc, errors.Wrap(err, "failed to write image manifest")
	}
	return desc, nil
}

// convOCIV1LayersToZfile applys image layers based on the overlaybd baselayer and
// exports the layers based on zfile.
//
// NOTE: The first element of descs will be overlaybd baselayer.
func convOCIV1LayersToZfile(ctx context.Context, sn snapshots.Snapshotter, cs content.Store, baseLayer *layer, srcDescs []ocispec.Descriptor, fsType string, basePath string) ([]layer, error) {
	var (
		lastParentID string
		err          error
	)
	// init base layer
	if baseLayer != nil {
		lastParentID, err = applyOCIV1LayerInZfile(ctx, sn, cs, "", baseLayer.desc, nil, func(root string) error {
			f, err := ioutil.ReadDir(root)
			if err != nil {
				return err
			}

			if len(f) != 1 || f[0].IsDir() {
				return errors.Errorf("unexpected base layer tar[.gz]")
			}
			return os.Rename(filepath.Join(root, f[0].Name()), filepath.Join(root, "overlaybd.commit"))
		})
		if err != nil {
			return nil, err
		}
	} else {
		lastParentID, err = buildBaseLayerInZfile(ctx, sn, fsType, basePath)
		if err != nil {
			return nil, err
		}
	}

	var (
		commitLayers = make([]layer, len(srcDescs)+1)

		opts = []snapshots.Opt{
			snapshots.WithLabels(map[string]string{
				"containerd.io/snapshot/overlaybd.writable":     "dir",
				"containerd.io/snapshot/overlaybd/blob-fs-type": fsType,
			}),
		}
	)

	var sendToContentStore = func(ctx context.Context, snID string) (layer, error) {
		info, err := sn.Stat(ctx, snID)
		if err != nil {
			return emptyLayer, err
		}

		loader := newContentLoaderWithFsType(false, fsType, contentFile{
			info.Labels["containerd.io/snapshot/overlaybd.localcommitpath"],
			"overlaybd.commit"})
		return loader.Load(ctx, cs)
	}

	commitLayers[0], err = sendToContentStore(ctx, lastParentID)
	if err != nil {
		return nil, err
	}

	eg, ctx := errgroup.WithContext(ctx)
	for idx, desc := range srcDescs {
		lastParentID, err = applyOCIV1LayerInZfile(ctx, sn, cs, lastParentID, desc, opts, nil)
		if err != nil {
			return nil, err
		}

		idxI := idx + 1
		snID := lastParentID
		eg.Go(func() error {
			var err error
			commitLayers[idxI], err = sendToContentStore(ctx, snID)
			return err
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return commitLayers, nil
}

// applyOCIV1LayerInZfile applys the OCIv1 tarfile in zfile format and commit it.
func applyOCIV1LayerInZfile(
	ctx context.Context,
	sn snapshots.Snapshotter, cs content.Store,
	parentID string, // the ID of parent snapshot
	desc ocispec.Descriptor, // the descriptor of layer
	snOpts []snapshots.Opt, // apply for the commit snapshotter
	afterApply func(root string) error, // do something after apply tar stream
) (string, error) {

	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return emptyString, errors.Wrapf(err, "failed to get reader %s from content store", desc.Digest)
	}
	defer ra.Close()

	var (
		key    string
		mounts []mount.Mount
	)

	for {
		key = fmt.Sprintf(convSnapshotNameFormat, uniquePart())
		mounts, err = sn.Prepare(ctx, key, parentID, snOpts...)
		if err != nil {
			// retry other key
			if errdefs.IsAlreadyExists(err) {
				continue
			}
			return emptyString, errors.Wrapf(err, "failed to preprare snapshot %q", key)
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
			if rerr := sn.Remove(ctx, key); rerr != nil {
				log.G(ctx).WithError(rerr).WithField("key", key).Warnf("apply failure and failed to cleanup snapshot")
			}
		}
	}()

	rc, err = compression.DecompressStream(rc)
	if err != nil {
		return emptyString, errors.Wrap(err, "failed to detect layer mediatype")
	}

	if err = mount.WithTempMount(ctx, mounts, func(root string) error {
		_, err := archive.Apply(ctx, root, rc)
		if err == nil && afterApply != nil {
			err = afterApply(root)
		}
		return err
	}); err != nil {
		return emptyString, errors.Wrapf(err, "failed to apply layer in snapshot %s", key)
	}

	// Read any trailing data
	if _, err := io.Copy(ioutil.Discard, rc); err != nil {
		return emptyString, err
	}

	commitID := fmt.Sprintf(convSnapshotNameFormat, digester.Digest())
	if err = sn.Commit(ctx, commitID, key); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return emptyString, err
		}
	}

	rollback = err != nil
	return commitID, nil
}

func buildBaseLayerInZfile(ctx context.Context, sn snapshots.Snapshotter, fsType string, basePath string) (string, error) {
	var (
		key    string
		mounts []mount.Mount
		err    error
	)

	snOpts := []snapshots.Opt{
		snapshots.WithLabels(map[string]string{
			"containerd.io/snapshot/overlaybd.writable":     "dir",
			"containerd.io/snapshot/overlaybd/blob-fs-type": fsType,
			"containerd.io/snapshot/overlaybd.baselayer":    "baselayer",
		}),
	}

	var afterApply = func(root string) error {
		f, err := ioutil.ReadDir(root)
		if err != nil {
			return err
		}
		if len(f) != 1 || f[0].IsDir() {
			return errors.Errorf("unexpected base layer")
		}
		src, err := os.Open(filepath.Join(root, f[0].Name()))
		if err != nil {
			return err
		}
		defer src.Close()
		des, err := os.Create(basePath)
		if err != nil {
			return err
		}
		defer des.Close()
		_, err = io.Copy(des, src)
		return err
	}

	for {
		key = fmt.Sprintf(convSnapshotNameFormat, uniquePart())
		mounts, err = sn.Prepare(ctx, key, "", snOpts...)
		if err != nil {
			// retry other key
			if errdefs.IsAlreadyExists(err) {
				continue
			}
			return emptyString, errors.Wrapf(err, "failed to preprare snapshot %q", key)
		}

		break
	}

	var rollback = true

	defer func() {
		if rollback {
			if rerr := sn.Remove(ctx, key); rerr != nil {
				log.G(ctx).WithError(rerr).WithField("key", key).Warnf("apply failure and failed to cleanup snapshot")
			}
		}
	}()

	commitID := fmt.Sprintf(convSnapshotNameFormat, uniquePart())
	if err = sn.Commit(ctx, commitID, key); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return emptyString, err
		}
	}

	if err = mount.WithTempMount(ctx, mounts, func(root string) error {
		if err == nil && afterApply != nil {
			err = afterApply(root)
		}
		return err
	}); err != nil {
		return emptyString, errors.Wrapf(err, "failed to apply layer in snapshot %s", key)
	}

	rollback = err != nil
	return commitID, nil
}

func ensureImageExist(ctx context.Context, cli *containerd.Client, imageName string) (containerd.Image, error) {
	return cli.GetImage(ctx, imageName)
}

func currentPlatformManifest(ctx context.Context, cs content.Provider, img containerd.Image) (ocispec.Manifest, error) {
	return images.Manifest(ctx, cs, img.Target(), platforms.Default())
}

func createImage(ctx context.Context, is images.Store, img images.Image) error {
	for {
		if _, err := is.Create(ctx, img); err != nil {
			if !errdefs.IsAlreadyExists(err) {
				return err
			}

			if _, err := is.Update(ctx, img); err != nil {
				if errdefs.IsNotFound(err) {
					continue
				}
				return err
			}
		}
		return nil
	}
}

// NOTE: based on https://github.com/containerd/containerd/blob/v1.4.3/rootfs/apply.go#L181-L187
func uniquePart() string {
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
