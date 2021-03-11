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
	emptyString      string
	emptyDesc        ocispec.Descriptor
	emptyLayer       layer
	emptyCompression compression.Compression

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
	},
	Action: func(context *cli.Context) error {
		var (
			srcImage    = context.Args().First()
			targetImage = context.Args().Get(1)
		)

		if srcImage == "" || targetImage == "" {
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

		srcImg, err := ensureImageExist(ctx, cli, srcImage)
		if err != nil {
			return err
		}

		srcManifest, err := currentPlatformManifest(ctx, cs, srcImg)
		if err != nil {
			return errors.Wrapf(err, "failed to read manifest")
		}

		baseLayer, err := loadCommittedSnapshotterInContent(ctx, cs, context.String("basepath"))
		if err != nil {
			return errors.Wrap(err, "failed to load baselayer into content.Store")
		}

		committedLayers, err := convOCIV1LayersToZfile(ctx, sn, cs, baseLayer, srcManifest.Layers)
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
func convOCIV1LayersToZfile(ctx context.Context, sn snapshots.Snapshotter, cs content.Store, baseLayer layer, srcDescs []ocispec.Descriptor) ([]layer, error) {
	// init base layer
	lastParentID, err := applyOCIV1LayerInZfile(ctx, sn, cs, "", baseLayer.desc, nil, func(root string) error {
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

	var (
		commitLayers = make([]layer, len(srcDescs)+1)

		opts = []snapshots.Opt{
			snapshots.WithLabels(map[string]string{
				"containerd.io/snapshot/overlaybd.writable": "from-ctr-build",
			}),
		}
	)

	var sendToContentStore = func(ctx context.Context, snID string) (layer, error) {
		info, err := sn.Stat(ctx, snID)
		if err != nil {
			return emptyLayer, err
		}

		commitPath := info.Labels["containerd.io/snapshot/overlaybd.localcommitpath"]
		return loadCommittedSnapshotterInContent(ctx, cs, commitPath)
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

// loadCommittedSnapshotterInContent uploads the commit data in content.Store service.
func loadCommittedSnapshotterInContent(ctx context.Context, cs content.Store, commitPath string) (layer, error) {
	labels := map[string]string{
		"containerd.io/snapshot/overlaybd/build.layer-from": commitPath,
	}

	basef, err := os.Open(commitPath)
	if err != nil {
		return emptyLayer, errors.Wrapf(err, "failed to locate commit data from %s", commitPath)
	}
	defer basef.Close()

	refName := fmt.Sprintf(convContentNameFormat, uniquePart())
	cw, err := content.OpenWriter(ctx, cs, content.WithRef(refName))
	if err != nil {
		return emptyLayer, errors.Wrap(err, "failed to open writer")
	}
	defer cw.Close()

	buf, comp, err := detectCompression(basef)
	if err != nil {
		return emptyLayer, errors.Wrapf(err, "failed to locate commit data from %s", commitPath)
	}

	digester := digest.Canonical.Digester()
	size := int64(0)

	var (
		uncompressedDigest digest.Digest
		mediatype          = ocispec.MediaTypeImageLayer
	)
	switch comp {
	case compression.Gzip:
		mediatype = ocispec.MediaTypeImageLayerGzip

		uncompressed := digest.Canonical.Digester()

		pipeR, pipeW := io.Pipe()
		errCh := make(chan error, 1)

		go func() (retErr error) {
			defer func() {
				errCh <- retErr
				close(errCh)
			}()

			dc, err := compression.DecompressStream(pipeR)
			if err != nil {
				return err
			}
			_, err = io.Copy(uncompressed.Hash(), dc)
			return err
		}()

		size, err = io.Copy(io.MultiWriter(cw, digester.Hash()), io.TeeReader(buf, pipeW))
		if err != nil {
			return emptyLayer, errors.Wrap(err, "failed to copy")
		}

		pipeW.Close()
		if err := <-errCh; err != nil {
			return emptyLayer, errors.Wrap(err, "failed to get uncompressed digest")
		}
		uncompressedDigest = uncompressed.Digest()
	case compression.Uncompressed:
		// FIXME(fuweid):
		//
		// by default, the base layer is in gzip format. For uncompressed
		// data, we assume that it is in zfile format which need be wrapped
		// by tar.
		fi, err := os.Stat(commitPath)
		if err != nil {
			return emptyLayer, err
		}

		wc := &writeCountWrapper{w: io.MultiWriter(cw, digester.Hash())}
		wcw := tar.NewWriter(wc)
		if err := wcw.WriteHeader(&tar.Header{
			Name:     "overlaybd.commit",
			Mode:     0444,
			Size:     fi.Size(),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return emptyLayer, err
		}

		if _, err := io.Copy(wcw, buf); err != nil {
			return emptyLayer, err
		}

		if err := wcw.Close(); err != nil {
			return emptyLayer, err
		}

		size = wc.c
		uncompressedDigest = digester.Digest()
	default:
	}

	dig := digester.Digest()
	if err := cw.Commit(ctx, size, dig, content.WithLabels(labels)); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return emptyLayer, errors.Wrapf(err, "failed commit")
		}
	}

	var (
		annoOverlayBDBlobDigest = "containerd.io/snapshot/overlaybd/blob-digest"
		annoOverlayBDBlobSize   = "containerd.io/snapshot/overlaybd/blob-size"
	)

	return layer{
		desc: ocispec.Descriptor{
			MediaType: mediatype,
			Digest:    dig,
			Size:      size,
			Annotations: map[string]string{
				annoOverlayBDBlobDigest: dig.String(),
				annoOverlayBDBlobSize:   fmt.Sprintf("%v", size),
			},
		},
		diffID: uncompressedDigest,
	}, nil
}

func detectCompression(f *os.File) (io.Reader, compression.Compression, error) {
	buf := bufio.NewReader(f)
	bs, err := buf.Peek(10)
	if err != nil && err != io.EOF {
		return nil, emptyCompression, err
	}
	return buf, compression.DetectCompression(bs), nil
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
