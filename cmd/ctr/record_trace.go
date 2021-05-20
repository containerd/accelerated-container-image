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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/containerd/console"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cmd/ctr/commands"
	ctrcontent "github.com/containerd/containerd/cmd/ctr/commands/content"
	"github.com/containerd/containerd/cmd/ctr/commands/tasks"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/defaults"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/platforms"
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
	uniqueObjectFormat = "record-trace-%d-%s"
	networkNamespace   = "record-trace"
	namespacePath      = "/var/run/netns/" + networkNamespace
	cniConf            = `{
  "cniVersion": "0.4.0",
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
)

var recordTraceCommand = cli.Command{
	Name:           "record-trace",
	Usage:          "record trace for prefetch",
	UsageText:      "Make an new image from the old one, with a trace layer on top of it. A temporary container will be created and do the recording.",
	ArgsUsage:      "[flags] OldImage NewImage [COMMAND] [ARG...]",
	SkipArgReorder: true,
	Flags: []cli.Flag{
		cli.UintFlag{
			Name:  "time",
			Usage: "record time in seconds. When time expires, a TERM signal will be sent to the task. The task might fail to respond signal if time is too short.",
			Value: 600,
		},
		cli.StringFlag{
			Name:  "user,u",
			Usage: "user[:password] Registry user and password",
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
			Value: defaults.DefaultRuntime,
		},
		cli.IntFlag{
			Name:  "max-concurrent-downloads",
			Usage: "Set the max concurrent downloads for each pull",
			Value: 8,
		},
		cli.BoolFlag{
			Name:  "tty,t",
			Usage: "allocate a TTY for the container",
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
		// Create client
		client, ctx, cancel, err := commands.NewClient(cliCtx)
		if err != nil {
			return err
		}
		defer cancel()
		cs := client.ContentStore()

		var con console.Console
		if cliCtx.Bool("tty") {
			con = console.Current()
			defer con.Reset()
			if err := con.SetRaw(); err != nil {
				return err
			}
		}

		// Validate arguments
		ref := cliCtx.Args().Get(0)
		if ref == "" {
			return errors.New("image ref must be provided")
		}
		newRef := cliCtx.Args().Get(1)
		if newRef == "" {
			return errors.New("new image ref must be provided")
		}
		if _, err = client.ImageService().Get(ctx, ref); err == nil {
			return errors.Errorf("Please remove old image %s first", ref)
		} else if !errdefs.IsNotFound(err) {
			return errors.Errorf("Fail to lookup image %s", ref)
		}
		if _, err = client.ImageService().Get(ctx, newRef); err == nil {
			return errors.Errorf("New image %s exists", newRef)
		} else if !errdefs.IsNotFound(err) {
			return errors.Errorf("Fail to lookup image %s", newRef)
		}

		// Fetch image metadata by rpull
		fetchConfig, err := ctrcontent.NewFetchConfig(ctx, cliCtx)
		if err != nil {
			return err
		}
		if err := rpull(ctx, client, ref, cliCtx.String("snapshotter"), fetchConfig); err != nil {
			return errors.Wrapf(err, "Fail to pull image metadata")
		}

		// Get image instance
		imgInstance, err := client.ImageService().Get(ctx, ref)
		if err != nil {
			return err
		}
		image := containerd.NewImage(client, imgInstance)
		imageManifest, err := images.Manifest(ctx, cs, image.Target(), platforms.Default())
		if err != nil {
			return err
		}

		// Validate top layer
		topLayer := imageManifest.Layers[len(imageManifest.Layers)-1]
		if _, ok := topLayer.Annotations["containerd.io/snapshot/overlaybd/blob-digest"]; !ok {
			return errors.New("Must be an overlaybd image")
		}
		if topLayer.Annotations["containerd.io/snapshot/overlaybd/acceleration-layer"] == "yes" {
			return errors.New("Acceleration layer already exists")
		}

		// Fetch all layer blobs into content
		if _, err = ctrcontent.Fetch(ctx, client, ref, fetchConfig); err != nil {
			return err
		}

		// Create trace file
		if err := os.Mkdir(cliCtx.String("working-dir"), 0644); err != nil && !os.IsExist(err) {
			return errors.Wrapf(err, "failed to create working dir")
		}
		traceFile := filepath.Join(cliCtx.String("working-dir"), uniqueObjectString())
		if _, err := os.Create(traceFile); err != nil {
			return errors.New("failed to create trace file")
		}
		defer os.Remove(traceFile)

		// Create lease
		ctx, deleteLease, err := client.WithLease(ctx,
			leases.WithID(uniqueObjectString()),
			leases.WithExpiration(1*time.Hour),
		)
		if err != nil {
			return errors.Wrap(err, "failed to create lease")
		}
		defer deleteLease(ctx)

		// Create isolated network
		if !cliCtx.Bool("disable-network-isolation") {
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

		task, err := tasks.NewTask(ctx, client, container, "", con, false, "", nil)
		if err != nil {
			return err
		}
		defer task.Delete(ctx)

		if cliCtx.Bool("tty") {
			if err := tasks.HandleConsoleResize(ctx, task, con); err != nil {
				return errors.Wrapf(err, "failed to resize console")
			}
		}

		var statusC <-chan containerd.ExitStatus
		if statusC, err = task.Wait(ctx); err != nil {
			return err
		}

		if err := task.Start(ctx); err != nil {
			return err
		}
		fmt.Println("Task is running ...")

		// Start a thread to watch timeout and signals, and control the termination of recording
		recordingStopped := make(chan bool)
		if !cliCtx.Bool("tty") {
			go recordingControl(ctx, cliCtx, task, traceFile, recordingStopped)
		}

		// Wait task stopped
		status := <-statusC
		code, _, err := status.Result()
		if err != nil {
			return err
		}
		if code != 0 {
			return cli.NewExitError("", int(code))
		}

		// Wait control thread stopped
		if !cliCtx.Bool("tty") {
			<-recordingStopped
		}

		// Load trace file into content, and generate an acceleration layer
		loader := newContentLoader(true, contentFile{traceFile, "trace"})
		accelLayer, err := loader.Load(ctx, cs)
		if err != nil {
			return fmt.Errorf("loadCommittedSnapshotInContent failed: %v", err)
		}

		// Create image with the acceleration layer on top
		newManifestDesc, err := createImageWithAccelLayer(ctx, cs, imageManifest, accelLayer)
		if err != nil {
			return fmt.Errorf("createImageWithAccelLayer failed: %v", err)
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

func recordingControl(ctx context.Context, cliCtx *cli.Context, task containerd.Task, traceFile string, recordingStopped chan bool) {
	lockFile := traceFile + ".lock"
	okFile := traceFile + ".ok"
	signalCancel := make(chan bool)

	// Allow termination by user signals
	sigc := registerSignals(ctx, task, signalCancel)

	select {
	case <-signalCancel:
		break
	case <-time.After(time.Duration(cliCtx.Uint("time")) * time.Second):
		fmt.Println("Timeout, stop recording ...")
		break
	}

	signal.Stop(sigc)
	close(sigc)

	if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Remove lock file %s failed: %v\n", lockFile, err)
		return
	}

	for {
		time.Sleep(time.Second)
		if _, err := os.Stat(okFile); err == nil {
			fmt.Printf("Found OK file %s, recording finished\n", okFile)
			_ = os.Remove(okFile)
			break
		}
	}

	st, err := task.Status(ctx)
	if err != nil {
		fmt.Printf("Failed to get task status: %v\n", err)
	}
	if st.Status == containerd.Running {
		if err = task.Kill(ctx, unix.SIGTERM); err != nil {
			fmt.Printf("Failed to kill task: %v\n", err)
		}
	}

	recordingStopped <- true
}

func createImageWithAccelLayer(ctx context.Context, cs content.Store, oldManifest ocispec.Manifest, l layer) (ocispec.Descriptor, error) {
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
	oldConfig.RootFS.DiffIDs = append(oldConfig.RootFS.DiffIDs, l.diffID)
	oldConfig.Created = &buildTime

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
	newManifest.Layers = append(oldManifest.Layers, l.desc)

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
	)
	cOpts = append(cOpts, containerd.WithContainerLabels(commands.LabelArgs(cliCtx.StringSlice("label"))))

	var (
		key = uniqueObjectString()
		// Specify the binary and arguments for the application to execute
		args = cliCtx.Args()[2:]
	)
	opts = append(opts, oci.WithDefaultSpec(), oci.WithDefaultUnixDevices)
	opts = append(opts, oci.WithImageConfig(image))
	cOpts = append(cOpts,
		containerd.WithImage(image),
		containerd.WithSnapshotter(cliCtx.String("snapshotter")))

	cOpts = append(cOpts, withNewSnapshot(key, image, cliCtx.String("snapshotter"), traceFile))
	cOpts = append(cOpts, containerd.WithImageStopSignal(image, "SIGTERM"))

	if !cliCtx.Bool("disable-network-isolation") {
		opts = append(opts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: namespacePath,
		}))
	}

	if cliCtx.Bool("tty") {
		opts = append(opts, oci.WithTTY)
	}

	if len(args) > 0 {
		opts = append(opts, oci.WithProcessArgs(args...))
	}

	cOpts = append(cOpts, containerd.WithRuntime(cliCtx.String("runtime"), nil))
	opts = append(opts, oci.WithAnnotations(commands.LabelArgs(cliCtx.StringSlice("label"))))
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
	lookupCmd := fmt.Sprintf("ip netns list | grep %s", networkNamespace)
	if err := exec.Command("bash", "-c", lookupCmd).Run(); err != nil {
		if err = exec.Command("ip", "netns", "add", networkNamespace).Run(); err != nil {
			return nil, errors.Wrapf(err, "failed to add netns")
		}
	}
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
	t := time.Now()
	var b [3]byte
	rand.Read(b[:])
	return fmt.Sprintf(uniqueObjectFormat, t.Unix(), hex.EncodeToString(b[:]))
}

func registerSignals(ctx context.Context, task containerd.Task, timeoutChan chan bool) chan os.Signal {
	sigc := make(chan os.Signal, 128)
	signal.Notify(sigc)
	go func() {
		for s := range sigc {
			if canIgnoreSignal(s) {
				continue
			}
			if s == unix.SIGTERM || s == unix.SIGINT {
				timeoutChan <- true
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
