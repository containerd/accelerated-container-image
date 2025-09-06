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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	obdconv "github.com/containerd/accelerated-container-image/pkg/convertor"
	"github.com/containerd/accelerated-container-image/pkg/label"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/cmd/ctr/commands"
	"github.com/containerd/containerd/v2/cmd/ctr/commands/tasks"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/errdefs"
	"github.com/containerd/go-cni"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/urfave/cli/v2"
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
	emptyDesc           ocispec.Descriptor
)

var recordTraceCommand = &cli.Command{
	Name:        "record-trace",
	Usage:       "record trace for prefetch",
	ArgsUsage:   "OldImage NewImage [COMMAND] [ARG...]",
	Description: "Make an new image from the old one, with a trace layer on top of it. A temporary container will be created and do the recording.",
	Flags: []cli.Flag{
		&cli.UintFlag{
			Name:  "time",
			Usage: "record time in seconds. When time expires, a TERM signal will be sent to the task. The task might fail to respond signal if time is too short.",
			Value: 60,
		},
		&cli.StringFlag{
			Name:  "priority_list",
			Usage: "path of a file-list contains files to be prefetched",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "working-dir",
			Value: "/tmp/ctr-record-trace/",
			Usage: "temporary working dir for trace files",
		},
		&cli.StringFlag{
			Name:  "snapshotter",
			Usage: "snapshotter name.",
			Value: "overlaybd",
		},
		&cli.StringFlag{
			Name:  "runtime",
			Usage: "runtime name",
			Value: string(plugins.RuntimePluginV2),
		},
		&cli.IntFlag{
			Name:  "max-concurrent-downloads",
			Usage: "Set the max concurrent downloads for each pull",
			Value: 8,
		},
		&cli.BoolFlag{
			Name:  "disable-network-isolation",
			Usage: "Do not use cni to provide network isolation, default is false",
		},
		&cli.StringFlag{
			Name:  "cni-plugin-dir",
			Usage: "cni plugin dir",
			Value: "/opt/cni/bin/",
		},
	},
	Before: func(cliCtx *cli.Context) error {
		if cliCtx.IsSet("priority_list") && cliCtx.Args().Len() > 2 {
			return errors.New("command args and priority_list can't be set at the same time")
		}
		return nil
	},
	Action: func(cliCtx *cli.Context) (err error) {
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
			return fmt.Errorf("few image %s exists", newRef)
		} else if !errdefs.IsNotFound(err) {
			return fmt.Errorf("fail to lookup image %s", newRef)
		}

		// Get image instance
		imgModel, err := client.ImageService().Get(ctx, ref)
		if err != nil {
			return fmt.Errorf("fail to get image %s", ref)
		}
		image := containerd.NewImage(client, imgModel)
		imageManifest, err := images.Manifest(ctx, cs, image.Target(), platforms.Default())
		if err != nil {
			return err
		}

		// Validate top layer. Get fs type
		topLayer := imageManifest.Layers[len(imageManifest.Layers)-1]
		if _, ok := topLayer.Annotations[label.OverlayBDBlobDigest]; !ok {
			return errors.New("Must be an overlaybd image")
		}
		if topLayer.Annotations[label.AccelerationLayer] == "yes" {
			return errors.New("Acceleration layer already exists")
		}
		fsType, ok := topLayer.Annotations[label.OverlayBDBlobFsType]
		if !ok {
			fsType = ""
		}

		// Create trace file
		if err := os.Mkdir(cliCtx.String("working-dir"), 0644); err != nil && !os.IsExist(err) {
			return fmt.Errorf("failed to create working dir: %w", err)
		}
		traceFile := filepath.Join(cliCtx.String("working-dir"), uniqueObjectString())
		var traceFd *os.File
		if traceFd, err = os.Create(traceFile); err != nil {
			return errors.New("failed to create trace file")
		}
		defer os.Remove(traceFile)
		if !cliCtx.IsSet("priority_list") {
			_ = traceFd.Close()

			// Create lease
			ctx, deleteLease, err := client.WithLease(ctx,
				leases.WithID(uniqueObjectString()),
				leases.WithExpiration(maxLeaseTime),
			)
			if err != nil {
				return fmt.Errorf("failed to create lease: %w", err)
			}
			defer deleteLease(ctx)

			// Create isolated network
			if !cliCtx.Bool("disable-network-isolation") {
				networkNamespace = uniqueObjectString()
				namespacePath = "/var/run/netns/" + networkNamespace
				if err = exec.Command("ip", "netns", "add", networkNamespace).Run(); err != nil {
					return fmt.Errorf("failed to add netns: %w", err)
				}
				defer func() {
					if nextErr := exec.Command("ip", "netns", "delete", networkNamespace).Run(); err == nil && nextErr != nil {
						err = fmt.Errorf("failed to delete netns: %w", nextErr)
					}
				}()
				cniObj, err := createIsolatedNetwork(cliCtx)
				if err != nil {
					return err
				}
				defer func() {
					if nextErr := cniObj.Remove(ctx, networkNamespace, namespacePath); err == nil && nextErr != nil {
						err = fmt.Errorf("failed to teardown network: %w", nextErr)
					}
				}()
				if _, err = cniObj.Setup(ctx, networkNamespace, namespacePath); err != nil {
					return fmt.Errorf("failed to setup network for namespace: %w", err)
				}
			}

			// Create container and run task
			fmt.Println("Create container")
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
				return fmt.Errorf("failed to get exit status: %w", err)
			}

			if timer.Stop() {
				watchStop <- true
				fmt.Println("Task finished before timeout ...")
			}

			// Collect trace
			if err = collectTrace(traceFile); err != nil {
				return err
			}
		} else {
			fmt.Println("Set priority list as acceleration layer")
			defer traceFd.Close()
			fn := cliCtx.String("priority_list")
			inf, err := os.OpenFile(fn, os.O_RDONLY, 0644)
			if err != nil {
				fmt.Printf("failed to open priority list: %s", err.Error())
				return err
			}
			defer inf.Close()
			_, err = io.Copy(traceFd, inf)
			if err != nil {
				return err
			}
		}

		// Load trace file into content, and generate an acceleration layer
		loader := obdconv.NewContentLoaderWithFsType(true, fsType, obdconv.ContentFile{SrcFilePath: traceFile, DstFileName: "trace"})
		accelLayer, err := loader.Load(ctx, cs)
		if err != nil {
			return fmt.Errorf("loadCommittedSnapshotInContent failed: %v", err)
		}

		// Create image with the acceleration layer on top
		newManifestDesc, err := createImageWithAccelLayer(ctx, cs, imageManifest, accelLayer)
		if err != nil {
			return fmt.Errorf("createImageWithAccelLayer failed: %v", err)
		}

		imgName := cliCtx.Args().Get(1)
		is := client.ImageService()
		img := images.Image{
			Name:   imgName,
			Target: newManifestDesc,
		}
		for {
			if _, err := is.Create(ctx, img); err != nil {
				if !errdefs.IsAlreadyExists(err) {
					return fmt.Errorf("createImage failed: %v", err)
				}

				if _, err := is.Update(ctx, img); err != nil {
					if errdefs.IsNotFound(err) {
						continue
					}
					return fmt.Errorf("createImage failed: %v", err)
				}
			}
			fmt.Printf("New image %s is created\n", newRef)
			return nil
		}
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

func createImageWithAccelLayer(ctx context.Context, cs content.Store, oldManifest ocispec.Manifest, l obdconv.Layer) (ocispec.Descriptor, error) {
	oldConfigData, err := content.ReadBlob(ctx, cs, oldManifest.Config)
	if err != nil {
		return emptyDesc, err
	}

	var oldConfig ocispec.Image
	if err := json.Unmarshal(oldConfigData, &oldConfig); err != nil {
		return emptyDesc, err
	}

	buildTime := time.Now()
	var newHistory = []ocispec.History{{
		Created:   &buildTime,
		CreatedBy: "Acceleration Layer",
	}}
	oldConfig.History = append(newHistory, oldConfig.History...)
	oldConfig.RootFS.DiffIDs = append(oldConfig.RootFS.DiffIDs, l.DiffID)
	oldConfig.Created = &buildTime

	newConfigData, err := json.MarshalIndent(oldConfig, "", "   ")
	if err != nil {
		return emptyDesc, fmt.Errorf("failed to marshal image: %w", err)
	}

	newConfigDesc := ocispec.Descriptor{
		MediaType: oldManifest.Config.MediaType,
		Digest:    digest.Canonical.FromBytes(newConfigData),
		Size:      int64(len(newConfigData)),
	}
	ref := remotes.MakeRefKey(ctx, newConfigDesc)
	if err := content.WriteBlob(ctx, cs, ref, bytes.NewReader(newConfigData), newConfigDesc); err != nil {
		return emptyDesc, fmt.Errorf("failed to write image config: %w", err)
	}

	newManifest := ocispec.Manifest{}
	newManifest.SchemaVersion = oldManifest.SchemaVersion
	newManifest.Config = newConfigDesc
	newManifest.Layers = append(oldManifest.Layers, l.Desc)

	imageMediaType := oldManifest.MediaType

	// V2 manifest is not adopted in OCI spec yet, so follow the docker registry V2 spec here
	var newManifestV2 = struct {
		ocispec.Manifest
		MediaType string `json:"mediaType"`
	}{
		Manifest:  newManifest,
		MediaType: imageMediaType, // images.MediaTypeDockerSchema2Manifest,
	}

	newManifestData, err := json.MarshalIndent(newManifestV2, "", "   ")
	if err != nil {
		return emptyDesc, err
	}
	newManifestDesc := ocispec.Descriptor{
		MediaType: imageMediaType, // images.MediaTypeDockerSchema2Manifest,
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
		return emptyDesc, fmt.Errorf("failed to write image manifest: %w", err)
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
		args = cliCtx.Args().Slice()[2:]
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
		return nil, fmt.Errorf("failed to initialize cni library: %w", err)
	}
	if err = cniObj.Load(cni.WithConfListBytes([]byte(cniConf)), cni.WithLoNetwork); err != nil {
		return nil, fmt.Errorf("failed to load cni conf: %w", err)
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
			return fmt.Errorf("snapshotter %s was not found: %w", snapshotter, errdefs.ErrNotFound)
		}
		opt := snapshots.WithLabels(map[string]string{
			label.RecordTrace:     "yes",
			label.RecordTracePath: traceFile,
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
