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
	"context"
	"fmt"

	"github.com/containerd/accelerated-container-image/pkg/label"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/cmd/ctr/commands"
	ctrcontent "github.com/containerd/containerd/v2/cmd/ctr/commands/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/snapshots"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/urfave/cli/v2"
)

var rpullCommand = &cli.Command{
	Name:        "rpull",
	Usage:       "rpull an image from a remote for overlaybd snapshotter",
	ArgsUsage:   "[flags] <ref>",
	Description: `Fetch and prepare an image for use in containerd.`,
	Flags: append(append(commands.RegistryFlags, commands.LabelFlag),
		&cli.StringFlag{
			Name:    "snapshotter",
			Usage:   "snapshotter name. Empty value stands for the default value.",
			Value:   "overlaybd",
			EnvVars: []string{"CONTAINERD_SNAPSHOTTER"},
		},
		&cli.BoolFlag{
			Name:  "download-blobs",
			Usage: "download overlaybd blobs",
		},
	),
	Action: func(context *cli.Context) error {
		var (
			ref = context.Args().First()
		)
		if ref == "" {
			return fmt.Errorf("please provide an image reference to pull")
		}

		client, ctx, cancel, err := commands.NewClient(context)
		if err != nil {
			return err
		}
		defer cancel()

		ctx, done, err := client.WithLease(ctx)
		if err != nil {
			return err
		}
		defer done(ctx)

		config, err := ctrcontent.NewFetchConfig(ctx, context)
		if err != nil {
			return err
		}

		if err := rpull(ctx, client, ref, context.String("snapshotter"), config, context.Bool("download-blobs")); err != nil {
			return err
		}

		fmt.Println("done")
		return nil
	},
}

// rpull pulls image with special labels
//
// NOTE: Based on https://github.com/containerd/containerd/blob/v1.5.0-beta.2/cmd/ctr/commands/content/fetch.go#L148.
func rpull(ctx context.Context, client *containerd.Client, ref string, sn string, config *ctrcontent.FetchConfig, download bool) error {
	ongoing := ctrcontent.NewJobs(ref)

	pctx, stopProgress := context.WithCancel(ctx)
	progress := make(chan struct{})

	go func() {
		if config.ProgressOutput != nil {
			ctrcontent.ShowProgress(pctx, ongoing, client.ContentStore(), config.ProgressOutput)
		}
		close(progress)
	}()

	h := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		ongoing.Add(desc)
		return nil, nil
	})

	snLabels := map[string]string{label.TargetImageRef: ref}
	if download {
		snLabels[label.DownloadRemoteBlob] = "download"
	}

	labels := commands.LabelArgs(config.Labels)

	opts := []containerd.RemoteOpt{
		containerd.WithPullLabels(labels),
		containerd.WithResolver(config.Resolver),
		containerd.WithImageHandler(h),
		containerd.WithPullUnpack,
		containerd.WithPullSnapshotter(sn, snapshots.WithLabels(snLabels)),
	}

	if config.PlatformMatcher != nil {
		opts = append(opts, containerd.WithPlatformMatcher(config.PlatformMatcher))
	} else {
		for _, platform := range config.Platforms {
			opts = append(opts, containerd.WithPlatform(platform))
		}
	}

	_, err := client.Pull(pctx, ref, opts...)
	stopProgress()
	return err

}
