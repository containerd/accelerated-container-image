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
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	obdconv "github.com/containerd/accelerated-container-image/pkg/convertor"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cmd/ctr/commands"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/platforms"
	_ "github.com/go-sql-driver/mysql"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

var (
	emptyDesc  ocispec.Descriptor
	emptyLayer layer

	convSnapshotNameFormat = "overlaybd-conv-%s"
	convLeaseNameFormat    = convSnapshotNameFormat
)

var convertCommand = cli.Command{
	Name:        "obdconv",
	Usage:       "convert image layer into overlaybd format type",
	ArgsUsage:   "<src-image> <dst-image>",
	Description: `Export images to an OCI tar[.gz] into zfile format`,
	Flags: append(commands.RegistryFlags,
		cli.StringFlag{
			Name:  "fstype",
			Usage: "filesystem type(required), used to mount block device, support specifying mount options and mkfs options, separate fs type and options by ';', separate mount options by ',', separate mkfs options by ' '",
			Value: "ext4",
		},
		cli.StringFlag{
			Name:  "dbstr",
			Usage: "data base config string used for layer deduplication",
			Value: "",
		},
	),
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

		fsType := context.String("fstype")
		fmt.Printf("filesystem type: %s\n", fsType)
		dbstr := context.String("dbstr")
		if dbstr != "" {
			fmt.Printf("database config string: %s\n", dbstr)
		}

		srcImg, err := ensureImageExist(ctx, cli, srcImage)
		if err != nil {
			return err
		}

		srcManifest, err := currentPlatformManifest(ctx, cs, srcImg)
		if err != nil {
			return errors.Wrapf(err, "failed to read manifest")
		}

		resolver, err := commands.GetResolver(ctx, context)
		if err != nil {
			return err
		}

		c, err := obdconv.NewOverlaybdConvertor(ctx, cs, sn, resolver, targetImage, dbstr)
		if err != nil {
			return err
		}
		newMfstDesc, err := c.Convert(ctx, srcManifest, fsType)
		if err != nil {
			return err
		}

		return obdconv.CreateImage(ctx, cli.ImageService(), targetImage, newMfstDesc)
	},
}

type layer struct {
	desc   ocispec.Descriptor
	diffID digest.Digest
}

func ensureImageExist(ctx context.Context, cli *containerd.Client, imageName string) (containerd.Image, error) {
	return cli.GetImage(ctx, imageName)
}

func currentPlatformManifest(ctx context.Context, cs content.Provider, img containerd.Image) (ocispec.Manifest, error) {
	return images.Manifest(ctx, cs, img.Target(), platforms.Default())
}

// NOTE: based on https://github.com/containerd/containerd/blob/v1.4.3/rootfs/apply.go#L181-L187
func uniquePart() string {
	t := time.Now()
	var b [3]byte
	// Ignore read failures, just decreases uniqueness
	rand.Read(b[:])
	return fmt.Sprintf("%d-%s", t.Nanosecond(), strings.Replace(base64.URLEncoding.EncodeToString(b[:]), "_", "-", -1))
}
