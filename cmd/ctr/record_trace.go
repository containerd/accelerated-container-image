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
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cmd/ctr/commands"
	"github.com/containerd/containerd/cmd/ctr/commands/tasks"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/plugin"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/go-cni"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"golang.org/x/sys/unix"
)

const (
	uniqueObjectFormat = "record-trace-%s-%s"
	traceNameInLayer   = ".trace"
	cniConf            = `{
  "cniVersion": "0.3.1",
  "name": "record-trace-cni",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "r-trace-br0",
      "isGateway": false,
      "ipMasq": true,
      "promiscMode": true,
      "ipam": {
        "type": "host-local",
        "ranges": [
          [{
            "subnet": "10.99.0.0/16"
          }],
          [{
            "subnet": "2001:4860:4860::/64"
          }]
        ],
        "routes": [
        ]
      }
    }
  ]
}
`
	containerdDir = "/var/lib/containerd/"
	v1ContentsDir = "io.containerd.content.v1.content/blobs/"
)

var (
	networkNamespace    string
	namespacePath       string
	maxCollectTraceTime time.Duration // In case there are back-store bugs and the trace file is not generated
	maxTaskRunningTime  time.Duration // In case the task is not able to respond to SIGTERM
	maxLeaseTime        time.Duration // After which the temporary resources shall be cleaned
)

var recordTraceCommand = cli.Command{
	Name:           "record-trace",
	Usage:          "record trace for prefetch",
	ArgsUsage:      "OldImage NewImage [COMMAND] [ARG...]",
	Description:    "Make an new image from the old one, with a trace layer on top of it. A temporary container will be created and do the recording.",
	SkipArgReorder: true,
	Flags: []cli.Flag{
		cli.UintFlag{
			Name:  "time",
			Usage: "record time in seconds. When time expires, a TERM signal will be sent to the task. The task might fail to respond signal if time is too short.",
			Value: 60,
		},
		cli.StringFlag{
			Name:  "working-dir",
			Value: "/tmp/ctr-record-trace/",
			Usage: "temporary working dir for trace files",
		},
		cli.StringFlag{
			Name:  "snapshotter",
			Usage: "snapshotter name.",
			Value: "overlaybd",
		},
		cli.StringFlag{
			Name:  "runtime",
			Usage: "runtime name",
			Value: plugin.RuntimeLinuxV1,
		},
		cli.IntFlag{
			Name:  "max-concurrent-downloads",
			Usage: "Set the max concurrent downloads for each pull",
			Value: 8,
		},
		cli.BoolFlag{
			Name:  "disable-network-isolation",
			Usage: "Do not use cni to provide network isolation, default is false",
		},
		cli.StringFlag{
			Name:  "cni-plugin-dir",
			Usage: "cni plugin dir",
			Value: "/opt/cni/bin/",
		},
	},

	Action: func(cliCtx *cli.Context) (err error) {
		rand.Seed(time.Now().UnixNano())
		recordTime := time.Duration(cliCtx.Uint("time")) * time.Second
		if recordTime == 0 {
			return errors.New("time can't be 0")
		}
		maxTaskRunningTime = recordTime
		maxCollectTraceTime = recordTime
		maxLeaseTime = 3 * recordTime

		// Create client
		client, ctx, cancel, err := commands.NewClient(cliCtx)
		if err != nil {
			return err
		}
		defer cancel()
		cs := client.ContentStore()

		// Validate arguments
		ref := cliCtx.Args().Get(0)
		if ref == "" {
			return errors.New("image ref must be provided")
		}
		newRef := cliCtx.Args().Get(1)
		if newRef == "" {
			return errors.New("new image ref must be provided")
		}
		if _, err = client.ImageService().Get(ctx, newRef); err == nil {
			return errors.Errorf("few image %s exists", newRef)
		} else if !errdefs.IsNotFound(err) {
			return errors.Errorf("fail to lookup image %s", newRef)
		}

		// Get image instance
		imgModel, err := client.ImageService().Get(ctx, ref)
		if err != nil {
			return errors.Errorf("fail to get image %s", ref)
		}
		image := containerd.NewImage(client, imgModel)
		imageManifest, err := images.Manifest(ctx, cs, image.Target(), platforms.Default())
		if err != nil {
			return err
		}

		// Create trace file
		if err := os.Mkdir(cliCtx.String("working-dir"), 0644); err != nil && !os.IsExist(err) {
			return errors.Wrapf(err, "failed to create working dir")
		}
		traceFile := filepath.Join(cliCtx.String("working-dir"), uniqueObjectString())
		var traceFd *os.File
		if traceFd, err = os.Create(traceFile); err != nil {
			return errors.New("failed to create trace file")
		}
		_ = traceFd.Close()
		defer os.Remove(traceFile)

		// Create lease
		ctx, deleteLease, err := client.WithLease(ctx,
			leases.WithID(uniqueObjectString()),
			leases.WithExpiration(maxLeaseTime),
		)
		if err != nil {
			return errors.Wrap(err, "failed to create lease")
		}
		defer deleteLease(ctx)

		// Create isolated network
		if !cliCtx.Bool("disable-network-isolation") {
			networkNamespace = uniqueObjectString()
			namespacePath = "/var/run/netns/" + networkNamespace
			if err = exec.Command("ip", "netns", "add", networkNamespace).Run(); err != nil {
				return errors.Wrapf(err, "failed to add netns")
			}
			defer func() {
				if nextErr := exec.Command("ip", "netns", "delete", networkNamespace).Run(); err == nil && nextErr != nil {
					err = errors.Wrapf(err, "failed to delete netns")
				}
			}()
			cniObj, err := createIsolatedNetwork(cliCtx)
			if err != nil {
				return err
			}
			defer func() {
				if nextErr := cniObj.Remove(ctx, networkNamespace, namespacePath); err == nil && nextErr != nil {
					err = errors.Wrapf(nextErr, "failed to teardown network")
				}
			}()
			if _, err = cniObj.Setup(ctx, networkNamespace, namespacePath); err != nil {
				return errors.Wrapf(err, "failed to setup network for namespace")
			}
		}

		// Create container and run task
		container, err := createContainer(ctx, client, cliCtx, image, traceFile)
		if err != nil {
			return err
		}
		defer container.Delete(ctx, containerd.WithSnapshotCleanup)

		task, err := tasks.NewTask(ctx, client, container, "", nil, false, "", nil)
		if err != nil {
			return err
		}
		defer task.Delete(ctx)

		var statusC <-chan containerd.ExitStatus
		if statusC, err = task.Wait(ctx); err != nil {
			return err
		}

		if err := task.Start(ctx); err != nil {
			return err
		}
		fmt.Println("Task is running ...")

		timer := time.NewTimer(recordTime)
		watchStop := make(chan bool)

		// Start a thread to watch timeout and signals
		go watchThread(ctx, timer, task, watchStop)

		// Wait task stopped
		status := <-statusC
		if _, _, err := status.Result(); err != nil {
			return errors.Wrapf(err, "failed to get exit status")
		}

		if timer.Stop() {
			watchStop <- true
			fmt.Println("Task finished before timeout ...")
		}

		if err = collectTrace(traceFile); err != nil {
			return err
		}

		// Make a duplicated top layer and fill in trace
		newTopLayer, err := duplicateTopLayerWithTrace(ctx, cs, imageManifest, traceFile)
		if err != nil {
			return err
		}

		newManifestDesc, err := createImageWithNewTopLayer(ctx, cs, imageManifest, newTopLayer)
		if err != nil {
			return fmt.Errorf("createImageWithNewTopLayer failed: %v", err)
		}

		newImage := images.Image{
			Name:   cliCtx.Args().Get(1),
			Target: newManifestDesc,
		}
		if err = createImage(ctx, client.ImageService(), newImage); err != nil {
			return fmt.Errorf("createImage failed: %v", err)
		}
		fmt.Printf("New image %s is created\n", newRef)
		return nil
	},
}

func watchThread(ctx context.Context, timer *time.Timer, task containerd.Task, watchStop chan bool) {
	// Allow termination by user signals
	sigStop := make(chan bool)
	sigChan := registerSignalsForTask(ctx, task, sigStop)

	select {
	case <-sigStop:
		timer.Stop()
		break
	case <-watchStop:
		break
	case <-timer.C:
		fmt.Println("Timeout, stop recording ...")
		break
	}

	signal.Stop(sigChan)
	close(sigChan)

	st, err := task.Status(ctx)
	if err != nil {
		fmt.Printf("Failed to get task status: %v\n", err)
	}
	if st.Status == containerd.Running {
		// Note the task will not be deleted until the end
		go preventTaskHang(ctx, task)

		if err = task.Kill(ctx, unix.SIGTERM); err != nil {
			fmt.Printf("Failed to kill task: %v\n", err)
		}
	}
}

func preventTaskHang(ctx context.Context, task containerd.Task) {
	time.Sleep(maxTaskRunningTime)
	st, err := task.Status(ctx)
	if err != nil {
		fmt.Printf("Failed to get task status: %v\n", err)
	}
	if st.Status == containerd.Running {
		fmt.Println("Reached maxTaskRunningTime, force kill ...")
		_ = task.Kill(ctx, unix.SIGKILL)
	}
}

func collectTrace(traceFile string) error {
	sigStop := make(chan bool)
	sigChan := registerSignalsForTraceCollection(sigStop)

	defer func() {
		signal.Stop(sigChan)
		close(sigChan)
	}()

	lockFile := traceFile + ".lock"
	if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Remove lock file %s failed: %v\n", lockFile, err)
		return err
	}

	okFile := traceFile + ".ok"
	defer os.Remove(okFile)

	fmt.Println("Collecting trace ...")
	var elapsed time.Duration
	for {
		select {
		case <-sigStop:
			return errors.New("trace collection was canceled")
		default:
		}

		if elapsed < maxCollectTraceTime {
			elapsed += time.Second
			time.Sleep(time.Second)
		} else {
			return errors.New("trace collection was timed-out")
		}

		if _, err := os.Stat(okFile); err == nil {
			fmt.Printf("Found OK file, trace is available now at %s\n", traceFile)
			return nil
		}
	}
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(p []byte) (n int, err error) {
	n, err = w.writer.Write(p)
	w.count += int64(n)
	return
}

func duplicateTopLayerWithTrace(ctx context.Context, cs content.Store, imgManifest ocispec.Manifest, traceFile string) (l layer, err error) {
	configData, err := content.ReadBlob(ctx, cs, imgManifest.Config)
	if err != nil {
		return emptyLayer, err
	}
	config := ocispec.Image{}
	if err := json.Unmarshal(configData, &config); err != nil {
		return emptyLayer, err
	}
	diffID := config.RootFS.DiffIDs[len(config.RootFS.DiffIDs)-1]
	contentDigest := ""

	var walker content.WalkFunc = func(info content.Info) error {
		for k, v := range info.Labels {
			if contentDigest == "" && k == "containerd.io/uncompressed" && v == diffID.String() {
				contentDigest = info.Digest.String()
			}
		}
		return nil
	}
	if err = cs.Walk(ctx, walker); err != nil {
		return emptyLayer, errors.Wrapf(err, "walk contents failed")
	}

	contentFilePath := containerdDir + v1ContentsDir + strings.Replace(contentDigest, ":", "/", 1)
	tmpDirPath, err := ioutil.TempDir("", "top-layer")
	if err != nil {
		return emptyLayer, err
	}
	defer os.RemoveAll(tmpDirPath)

	if err = exec.Command("tar", "-C", tmpDirPath, "-xf", contentFilePath).Run(); err != nil {
		return emptyLayer, errors.Wrapf(err, "extract content failed")
	}

	if err = copyFile(traceFile, path.Join(tmpDirPath, traceNameInLayer)); err != nil {
		return emptyLayer, errors.Wrapf(err, "copy file failed")
	}

	contentWriter, err := content.OpenWriter(ctx, cs, content.WithRef(uniqueObjectString()))
	if err != nil {
		return emptyLayer, errors.Wrapf(err, "failed to open content writer")
	}
	defer contentWriter.Close()

	tarGzipDigester := digest.Canonical.Digester()
	tarGzipCountingWriter := &countingWriter{writer: io.MultiWriter(contentWriter, tarGzipDigester.Hash())}
	gzipWriter := gzip.NewWriter(tarGzipCountingWriter)

	tarDigester := digest.Canonical.Digester()
	tarWriter := tar.NewWriter(io.MultiWriter(gzipWriter, tarDigester.Hash()))

	filesInfo, err := ioutil.ReadDir(tmpDirPath)
	if err != nil {
		return emptyLayer, errors.Wrapf(err, "failed to read dir")
	}

	for _, info := range filesInfo {
		fd, err := os.Open(path.Join(tmpDirPath, info.Name()))
		if err != nil {
			return emptyLayer, errors.Wrapf(err, "failed to open file of %s", info.Name())
		}

		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     info.Name(),
			Mode:     0444,
			Size:     info.Size(),
			Typeflag: tar.TypeReg,
			ModTime:  info.ModTime(),
		}); err != nil {
			return emptyLayer, errors.Wrapf(err, "failed to write tar header")
		}

		if _, err := io.Copy(tarWriter, bufio.NewReader(fd)); err != nil {
			return emptyLayer, errors.Wrapf(err, "failed to copy IO")
		}
	}

	if err = tarWriter.Close(); err != nil {
		return emptyLayer, errors.Wrapf(err, "failed to close tar writer")
	}
	if err = gzipWriter.Close(); err != nil {
		return emptyLayer, errors.Wrapf(err, "failed to close gzip writer")
	}

	annotationsCopy := make(map[string]string)
	for k, v := range imgManifest.Layers[len(imgManifest.Layers)-1].Annotations {
		annotationsCopy[k] = v
	}

	if err := contentWriter.Commit(ctx, tarGzipCountingWriter.count, tarGzipDigester.Digest()); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return emptyLayer, errors.Wrapf(err, "failed to commit content")
		}
	}

	l = layer{
		desc: ocispec.Descriptor{
			MediaType:   images.MediaTypeDockerSchema2LayerGzip,
			Digest:      tarGzipDigester.Digest(),
			Size:        tarGzipCountingWriter.count,
			Annotations: annotationsCopy,
		},
		diffID: tarDigester.Digest(),
	}
	return l, nil
}

//func duplicateTopLayerWithTrace2(ctx context.Context, ss snapshots.Snapshotter, cs content.Store, imgManifest ocispec.Manifest, container containerd.Container, traceFile string) (l layer, err error) {
//	containerInfo, err := container.Info(ctx)
//	if err != nil {
//		return emptyLayer, err
//	}
//
//	snapshotInfo, err := ss.Stat(ctx, containerInfo.SnapshotKey)
//	if err != nil {
//		return emptyLayer, err
//	}
//	parentSnapshotInfo, err := ss.Stat(ctx, snapshotInfo.Parent)
//	if err != nil {
//		return emptyLayer, err
//	}
//
//	grandParentKey := parentSnapshotInfo.Parent
//	parentKey := snapshotInfo.Parent
//
//	lowerSnapshotKey := uniqueObjectString()
//	lowerMounts, err := ss.View(ctx, lowerSnapshotKey, grandParentKey)
//	if err != nil {
//		return emptyLayer, err
//	}
//	defer func() {
//		if nextErr := ss.Remove(ctx, lowerSnapshotKey); nextErr != nil && err == nil {
//			err = errors.Wrapf(nextErr, "failed to remove lower snapshot")
//		}
//	}()
//
//	upperSnapshotKey := uniqueObjectString()
//	upperMounts, err := ss.Prepare(ctx, upperSnapshotKey, parentKey)
//	if err != nil {
//		return emptyLayer, err
//	}
//	defer func() {
//		if nextErr := ss.Remove(ctx, upperSnapshotKey); nextErr != nil && err == nil {
//			err = errors.Wrapf(nextErr, "failed to remove upper snapshot")
//		}
//	}()
//	if len(upperMounts) != 1 {
//		return emptyLayer, errors.New("prepare upper returned unknown mounts")
//	}
//
//	upperdir := ""
//	for _, opt := range upperMounts[0].Options {
//		if strings.HasPrefix(opt, "upperdir=") {
//			upperdir = opt[len("upperdir="):]
//			break
//		}
//	}
//	if upperdir == "" {
//		return emptyLayer, errors.New("cannot find upperdir, unable to fill in trace file")
//	}
//
//	if err = copyFile(traceFile, path.Join(upperdir, traceNameInLayer)); err != nil {
//		return emptyLayer, err
//	}
//
//	diffComparer := walking.NewWalkingDiff(cs)
//	opt := diff.WithMediaType(ocispec.MediaTypeImageLayerGzip)
//	contentDesc, err := diffComparer.Compare(ctx, lowerMounts, upperMounts, opt)
//	if err != nil {
//		return emptyLayer, err
//	}
//	opt = diff.WithMediaType(ocispec.MediaTypeImageLayer)
//	contentDescUncompressed, err := diffComparer.Compare(ctx, lowerMounts, upperMounts, opt)
//	if err != nil {
//		return emptyLayer, err
//	}
//
//	annotationsCopy := make(map[string]string)
//	for k, v := range imgManifest.Layers[len(imgManifest.Layers)-1].Annotations {
//		annotationsCopy[k] = v
//	}
//
//	// Remain media type the same as the old images
//	contentDesc.MediaType = images.MediaTypeDockerSchema2LayerGzip
//	contentDesc.Annotations = annotationsCopy
//
//	l = layer{
//		desc:   contentDesc,
//		diffID: contentDescUncompressed.Digest,
//	}
//	return l, nil
//}

func createImageWithNewTopLayer(ctx context.Context, cs content.Store, oldManifest ocispec.Manifest, l layer) (ocispec.Descriptor, error) {
	oldConfigData, err := content.ReadBlob(ctx, cs, oldManifest.Config)
	if err != nil {
		return emptyDesc, err
	}

	oldConfig := make(map[string]interface{})
	if err := json.Unmarshal(oldConfigData, &oldConfig); err != nil {
		return emptyDesc, err
	}

	rootfs, ok := oldConfig["rootfs"].(map[string]interface{})
	if !ok {
		return emptyDesc, errors.New("failed to parse rootfs")
	}
	diffIds, ok := rootfs["diff_ids"].([]interface{})
	if !ok {
		return emptyDesc, errors.New("failed to parse diff_ids")
	}
	diffIds[len(diffIds)-1] = l.diffID.String()

	newConfigData, err := json.MarshalIndent(oldConfig, "", "   ")
	if err != nil {
		return emptyDesc, errors.Wrap(err, "failed to marshal image")
	}

	newConfigDesc := ocispec.Descriptor{
		MediaType: oldManifest.Config.MediaType,
		Digest:    digest.Canonical.FromBytes(newConfigData),
		Size:      int64(len(newConfigData)),
	}
	ref := remotes.MakeRefKey(ctx, newConfigDesc)
	if err := content.WriteBlob(ctx, cs, ref, bytes.NewReader(newConfigData), newConfigDesc); err != nil {
		return emptyDesc, errors.Wrap(err, "failed to write image config")
	}

	newManifest := ocispec.Manifest{}
	newManifest.SchemaVersion = oldManifest.SchemaVersion
	newManifest.Config = newConfigDesc
	newManifest.Layers = append(oldManifest.Layers[:len(oldManifest.Layers)-1], l.desc)

	// V2 manifest is not adopted in OCI spec yet, so follow the docker registry V2 spec here
	var newManifestV2 = struct {
		ocispec.Manifest
		MediaType string `json:"mediaType"`
	}{
		Manifest:  newManifest,
		MediaType: images.MediaTypeDockerSchema2Manifest,
	}

	newManifestData, err := json.MarshalIndent(newManifestV2, "", "   ")
	if err != nil {
		return emptyDesc, err
	}

	newManifestDesc := ocispec.Descriptor{
		MediaType: images.MediaTypeDockerSchema2Manifest,
		Digest:    digest.Canonical.FromBytes(newManifestData),
		Size:      int64(len(newManifestData)),
	}

	labels := map[string]string{}
	labels["containerd.io/gc.ref.content.config"] = newConfigDesc.Digest.String()
	for i, layer := range newManifest.Layers {
		labels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = layer.Digest.String()
	}

	ref = remotes.MakeRefKey(ctx, newManifestDesc)
	if err := content.WriteBlob(ctx, cs, ref, bytes.NewReader(newManifestData), newManifestDesc, content.WithLabels(labels)); err != nil {
		return emptyDesc, errors.Wrap(err, "failed to write image manifest")
	}
	return newManifestDesc, nil
}

func createContainer(ctx context.Context, client *containerd.Client, cliCtx *cli.Context, image containerd.Image, traceFile string) (containerd.Container, error) {
	snapshotter := cliCtx.String("snapshotter")
	unpacked, err := image.IsUnpacked(ctx, snapshotter)
	if err != nil {
		return nil, err
	}
	if !unpacked {
		if err := image.Unpack(ctx, snapshotter); err != nil {
			return nil, err
		}
	}

	var (
		opts  []oci.SpecOpts
		cOpts []containerd.NewContainerOpts
		spec  containerd.NewContainerOpts

		key = uniqueObjectString()
		// Specify the binary and arguments for the application to execute
		args = cliCtx.Args()[2:]
	)

	cOpts = append(cOpts,
		containerd.WithImage(image),
		containerd.WithSnapshotter(cliCtx.String("snapshotter")),
		containerd.WithImageStopSignal(image, "SIGTERM"),
		containerd.WithRuntime(cliCtx.String("runtime"), nil),
		withNewSnapshot(key, image, cliCtx.String("snapshotter"), traceFile),
	)

	opts = append(opts,
		oci.WithDefaultSpec(),
		oci.WithDefaultUnixDevices,
		oci.WithImageConfig(image),
	)
	if !cliCtx.Bool("disable-network-isolation") {
		opts = append(opts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: namespacePath,
		}))
	}

	if len(args) > 0 {
		opts = append(opts, oci.WithProcessArgs(args...))
	}

	var s specs.Spec
	spec = containerd.WithSpec(&s, opts...)
	cOpts = append(cOpts, spec)

	// oci.WithImageConfig (WithUsername, WithUserID) depends on access to rootfs for resolving via
	// the /etc/{passwd,group} files. So cOpts needs to have precedence over opts.
	container, err := client.NewContainer(ctx, key, cOpts...)
	if err != nil {
		return nil, err
	}
	return container, nil
}

func createIsolatedNetwork(cliCtx *cli.Context) (cni.CNI, error) {
	cniObj, err := cni.New(
		cni.WithMinNetworkCount(2),
		cni.WithPluginDir([]string{cliCtx.String("cni-plugin-dir")}),
	)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to initialize cni library")
	}
	if err = cniObj.Load(cni.WithConfListBytes([]byte(cniConf)), cni.WithLoNetwork); err != nil {
		return nil, errors.Wrapf(err, "failed to load cni conf")
	}
	return cniObj, nil
}

func withNewSnapshot(key string, img containerd.Image, snapshotter, traceFile string) containerd.NewContainerOpts {
	return func(ctx context.Context, client *containerd.Client, c *containers.Container) error {
		diffIDs, err := img.RootFS(ctx)
		if err != nil {
			return err
		}
		parent := identity.ChainID(diffIDs).String()

		s := client.SnapshotService(snapshotter)
		if s == nil {
			return errors.Wrapf(errdefs.ErrNotFound, "snapshotter %s was not found", snapshotter)
		}
		opt := snapshots.WithLabels(map[string]string{
			"containerd.io/snapshot/overlaybd/record-trace":      "yes",
			"containerd.io/snapshot/overlaybd/record-trace-path": traceFile,
		})
		if _, err := s.Prepare(ctx, key, parent, opt); err != nil {
			return err
		}

		c.SnapshotKey = key
		c.Snapshotter = snapshotter
		c.Image = img.Name()
		return nil
	}
}

func uniqueObjectString() string {
	now := time.Now()
	var b [2]byte
	rand.Read(b[:])
	return fmt.Sprintf(uniqueObjectFormat, now.Format("20060102150405"), hex.EncodeToString(b[:]))
}

func registerSignalsForTraceCollection(sigStop chan bool) chan os.Signal {
	sigc := make(chan os.Signal, 128)
	signal.Notify(sigc)
	go func() {
		for s := range sigc {
			if canIgnoreSignal(s) {
				continue
			}
			if s == unix.SIGTERM || s == unix.SIGINT {
				sigStop <- true
			}
		}
	}()
	return sigc
}

func registerSignalsForTask(ctx context.Context, task containerd.Task, sigStop chan bool) chan os.Signal {
	sigc := make(chan os.Signal, 128)
	signal.Notify(sigc)
	go func() {
		for s := range sigc {
			if canIgnoreSignal(s) {
				continue
			}
			if s == unix.SIGTERM || s == unix.SIGINT {
				fmt.Printf("Received signal %s\n", s)
				sigStop <- true
			}
			// Forward all signals to task
			if err := task.Kill(ctx, s.(syscall.Signal)); err != nil {
				if errdefs.IsNotFound(err) {
					return
				}
				fmt.Printf("failed to forward signal %s\n", s)
			}
		}
	}()
	return sigc
}

// Go 1.14 started to use non-cooperative goroutine preemption, SIGURG should be ignored
func canIgnoreSignal(s os.Signal) bool {
	return s == unix.SIGURG
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err = out.Truncate(0); err != nil {
		return err
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	err = out.Sync()
	return err
}
