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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"

	"github.com/containerd/accelerated-container-image/pkg/snapshot"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/continuity"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	repo      string
	user      string
	plain     bool
	tagInput  string
	tagOutput string
	dir       string

	rootCmd = &cobra.Command{
		Use:   "overlaybd-convertor",
		Short: "An image conversion tool from oci image to overlaybd image.",
		Long:  "overlaybd-convertor is a standalone userspace image conversion tool that helps converting oci images to overlaybd images",
		Run: func(cmd *cobra.Command, args []string) {
			if err := convert(); err != nil {
				logrus.Errorf("run with error: %v", err)
				os.Exit(1)
			}
		},
	}
)

func prepareWritableLayer(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-create")
	dataPath := path.Join(dir, "writable_data")
	indexPath := path.Join(dir, "writable_index")
	os.RemoveAll(dataPath)
	os.RemoveAll(indexPath)
	out, err := exec.CommandContext(ctx, binpath,
		dataPath, indexPath, "64").CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to prepare writable layer: %s", out)
	}
	return nil
}

func writeConfig(dir string, configJSON *snapshot.OverlayBDBSConfig) error {
	data, err := json.Marshal(configJSON)
	if err != nil {
		return err
	}

	confPath := path.Join(dir, "config.json")
	if err := continuity.AtomicWriteFile(confPath, data, 0600); err != nil {
		return err
	}
	return nil
}

func overlaybdApply(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-apply")

	out, err := exec.CommandContext(ctx, binpath,
		path.Join(dir, "layer.tar"),
		path.Join(dir, "config.json")).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to apply tar to overlaybd: %s", out)
	}
	return nil
}

func overlaybdCommit(ctx context.Context, dir string) error {
	binpath := filepath.Join("/opt/overlaybd/bin", "overlaybd-commit")

	out, err := exec.CommandContext(ctx, binpath, "-z",
		path.Join(dir, "writable_data"),
		path.Join(dir, "writable_index"),
		path.Join(dir, "overlaybd.commit")).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to commit overlaybd: %s", out)
	}
	return nil
}

func makeDesc(dir string) (specs.Descriptor, error) {
	commitFile := path.Join(dir, "overlaybd.commit")
	file, err := os.Open(commitFile)
	if err != nil {
		return specs.Descriptor{}, err
	}
	defer file.Close()

	h := sha256.New()
	size, err := io.Copy(h, file)
	if err != nil {
		return specs.Descriptor{}, err
	}
	dgst := digest.NewDigest(digest.SHA256, h)
	return specs.Descriptor{
		MediaType: images.MediaTypeDockerSchema2Layer,
		Digest:    dgst,
		Size:      size,
		Annotations: map[string]string{
			"containerd.io/snapshot/overlaybd/blob-digest": dgst.String(),
			"containerd.io/snapshot/overlaybd/blob-size":   fmt.Sprintf("%d", size),
		},
	}, nil
}

func uploadBlob(ctx context.Context, pusher remotes.Pusher, path string, desc specs.Descriptor) error {
	cw, err := pusher.Push(ctx, desc)
	if err != nil {
		if errdefs.IsAlreadyExists(err) {
			logrus.Infof("layer %s exists", desc.Digest.String())
			return nil
		}
		return err
	}

	defer cw.Close()
	fobd, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fobd.Close()
	if err = content.Copy(ctx, cw, fobd, desc.Size, desc.Digest); err != nil {
		return err
	}
	return nil
}

func uploadBytes(ctx context.Context, pusher remotes.Pusher, desc specs.Descriptor, data []byte) error {
	cw, err := pusher.Push(ctx, desc)
	if err != nil {
		if errdefs.IsAlreadyExists(err) {
			logrus.Infof("content %s exists", desc.Digest.String())
			return nil
		}
		return err
	}
	defer cw.Close()

	err = content.Copy(ctx, cw, bytes.NewReader(data), desc.Size, desc.Digest)
	if err != nil {
		return err
	}
	return nil
}

func convert() error {
	ctx := context.Background()
	defer func() {
		// clean temp data
		os.RemoveAll(dir)
	}()

	resolver := docker.NewResolver(docker.ResolverOptions{
		Credentials: func(s string) (string, string, error) {
			if user == "" {
				return "", "", nil
			}
			userSplit := strings.Split(user, ":")
			return userSplit[0], userSplit[1], nil
		},
		PlainHTTP: plain,
	})

	ref := repo + ":" + tagInput
	_, desc, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return errors.Wrapf(err, "failed to resolve reference %q", ref)
	}

	fetcher, err := resolver.Fetcher(ctx, ref)
	if err != nil {
		return errors.Wrapf(err, "failed to get fetcher for %q", ref)
	}

	targetRef := repo + ":" + tagOutput
	pusher, err := resolver.Pusher(ctx, targetRef)
	if err != nil {
		return errors.Wrapf(err, "failed to get pusher for %q", targetRef)
	}

	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return errors.Wrapf(err, "failed to fetch manifest")
	}
	buf, err := ioutil.ReadAll(rc)
	if err != nil {
		return errors.Wrapf(err, "failed to fetch manifest")
	}
	rc.Close()

	manifest := specs.Manifest{}
	err = json.Unmarshal(buf, &manifest)
	if err != nil {
		return err
	}

	rc, err = fetcher.Fetch(ctx, manifest.Config)
	if err != nil {
		return errors.Wrapf(err, "failed to fetch config")
	}
	buf, err = ioutil.ReadAll(rc)
	if err != nil {
		return errors.Wrapf(err, "failed to fetch config")
	}
	rc.Close()

	config := specs.Image{}
	if err = json.Unmarshal(buf, &config); err != nil {
		return err
	}

	configJSON := snapshot.OverlayBDBSConfig{
		Lowers:     []snapshot.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	configJSON.Lowers = append(configJSON.Lowers, snapshot.OverlayBDBSConfigLower{
		File: "/opt/overlaybd/baselayers/ext4_64",
	})

	lastDigest := ""
	for idx, layer := range manifest.Layers {
		rc, err := fetcher.Fetch(ctx, layer)
		if err != nil {
			return errors.Wrapf(err, "failed to download for layer %d", idx)
		}
		drc, err := compression.DecompressStream(rc)
		if err != nil {
			return errors.Wrapf(err, "failed to decompress for layer %d", idx)
		}
		layerDir := path.Join(dir, layer.Digest.String())
		if err = os.MkdirAll(layerDir, 0644); err != nil {
			return err
		}

		ftar, err := os.Create(path.Join(layerDir, "layer.tar"))
		if err != nil {
			return err
		}
		if _, err = io.Copy(ftar, drc); err != nil {
			return errors.Wrapf(err, "failed to decompress copy for layer %d", idx)
		}
		logrus.Infof("downloaded layer %d", idx)
		// TODO check diffID

		// make writable layer
		if err = prepareWritableLayer(ctx, layerDir); err != nil {
			return errors.Wrapf(err, "failed to overlaybd create for layer %d", idx)
		}

		// make config
		if idx > 0 {
			configJSON.Lowers = append(configJSON.Lowers, snapshot.OverlayBDBSConfigLower{
				File: path.Join(dir, lastDigest, "overlaybd.commit"),
			})
		}
		configJSON.Upper = snapshot.OverlayBDBSConfigUpper{
			Data:  path.Join(layerDir, "writable_data"),
			Index: path.Join(layerDir, "writable_index"),
		}
		if err = writeConfig(layerDir, &configJSON); err != nil {
			return err
		}

		// apply and commit
		if err = overlaybdApply(ctx, layerDir); err != nil {
			return errors.Wrapf(err, "failed to overlaybd apply for layer %d", idx)
		}
		if err = overlaybdCommit(ctx, layerDir); err != nil {
			return errors.Wrapf(err, "failed to overlaybd commit for layer %d", idx)
		}
		//for test
		// os.Rename(path.Join(layerDir, "layer.tar"), path.Join(layerDir, "overlaybd.commit"))

		//calc digest
		desc, err := makeDesc(layerDir)
		if err != nil {
			return errors.Wrapf(err, "failed to make descriptor for layer %d", idx)
		}

		// upload
		if err = uploadBlob(ctx, pusher, path.Join(layerDir, "overlaybd.commit"), desc); err != nil {
			return errors.Wrapf(err, "failed to upload layer %d", idx)
		}
		logrus.Infof("layer %d uploaded", idx)

		lastDigest = manifest.Layers[idx].Digest.String()
		manifest.Layers[idx] = desc
		config.RootFS.DiffIDs[idx] = desc.Digest
	}

	// add baselayer
	baseDesc := specs.Descriptor{
		MediaType: images.MediaTypeDockerSchema2Layer,
		Digest:    "sha256:c3a417552a6cf9ffa959b541850bab7d7f08f4255425bf8b48c85f7b36b378d9",
		Size:      4737695,
		Annotations: map[string]string{
			"containerd.io/snapshot/overlaybd/blob-digest": "sha256:c3a417552a6cf9ffa959b541850bab7d7f08f4255425bf8b48c85f7b36b378d9",
			"containerd.io/snapshot/overlaybd/blob-size":   "4737695",
		},
	}
	if err = uploadBlob(ctx, pusher, "/opt/overlaybd/baselayers/ext4_64", baseDesc); err != nil {
		return errors.Wrapf(err, "failed to upload baselayer")
	}
	manifest.Layers = append([]specs.Descriptor{baseDesc}, manifest.Layers...)
	config.RootFS.DiffIDs = append([]digest.Digest{baseDesc.Digest}, config.RootFS.DiffIDs...)

	// upload config and manifest
	cbuf, err := json.Marshal(config)
	if err != nil {
		return err
	}
	manifest.Config = specs.Descriptor{
		MediaType: images.MediaTypeDockerSchema2Config,
		Digest:    digest.FromBytes(cbuf),
		Size:      (int64)(len(cbuf)),
	}
	if err = uploadBytes(ctx, pusher, manifest.Config, cbuf); err != nil {
		return errors.Wrapf(err, "failed to upload config")
	}

	cbuf, err = json.Marshal(manifest)
	if err != nil {
		return err
	}
	manifestDesc := specs.Descriptor{
		MediaType: images.MediaTypeDockerSchema2Manifest,
		Digest:    digest.FromBytes(cbuf),
		Size:      (int64)(len(cbuf)),
	}

	if err = uploadBytes(ctx, pusher, manifestDesc, cbuf); err != nil {
		return errors.Wrapf(err, "failed to upload manifest")
	}
	logrus.Infof("convert finished")

	return nil
}

func init() {
	rootCmd.Flags().SortFlags = false
	rootCmd.Flags().StringVarP(&repo, "repository", "r", "", "repository for converting image (required)")
	rootCmd.Flags().StringVarP(&user, "username", "u", "", "user[:password] Registry user and password")
	rootCmd.Flags().BoolVarP(&plain, "plain", "", false, "connections using plain HTTP")
	rootCmd.Flags().StringVarP(&tagInput, "input-tag", "i", "", "tag for image converting from (required)")
	rootCmd.Flags().StringVarP(&tagOutput, "output-tag", "o", "", "tag for image converting to (required)")
	rootCmd.Flags().StringVarP(&dir, "dir", "d", "tmp_conv", "directory used for temporary data")

	rootCmd.MarkFlagRequired("repository")
	rootCmd.MarkFlagRequired("input-tag")
	rootCmd.MarkFlagRequired("output-tag")
}

func main() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	go func() {
		<-sigChan
		os.Exit(0)
	}()

	rootCmd.Execute()
}
