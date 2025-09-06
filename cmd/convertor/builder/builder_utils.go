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
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/containerd/continuity"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"

	t "github.com/containerd/accelerated-container-image/pkg/types"
)

func fetch(ctx context.Context, fetcher remotes.Fetcher, desc specs.Descriptor, target any) error {
	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("failed to fetch digest %v: %w", desc.Digest, err)
	}
	defer func() {
		rc.Close()
	}()

	buf, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("failed to read digest %v: %w", desc.Digest, err)
	}
	if err = json.Unmarshal(buf, target); err != nil {
		return fmt.Errorf("failed to unmarshal digest %v: %w", desc.Digest, err)
	}
	return nil
}

func fetchManifest(ctx context.Context, fetcher remotes.Fetcher, desc specs.Descriptor) (*specs.Manifest, error) {
	platformMatcher := platforms.Default()
	log.G(ctx).Infof("fetching manifest %v with type %v", desc.Digest, desc.MediaType)
	switch desc.MediaType {
	case images.MediaTypeDockerSchema2Manifest, specs.MediaTypeImageManifest:
		manifest := specs.Manifest{}
		if err := fetch(ctx, fetcher, desc, &manifest); err != nil {
			return nil, fmt.Errorf("failed to fetch manifest: %w", err)
		}
		return &manifest, nil
	case images.MediaTypeDockerSchema2ManifestList, specs.MediaTypeImageIndex:
		var target *specs.Descriptor
		manifestList := specs.Index{}
		if err := fetch(ctx, fetcher, desc, &manifestList); err != nil {
			return nil, fmt.Errorf("failed to fetch manifest list: %w", err)
		}
		for _, manifest := range manifestList.Manifests {
			if platformMatcher.Match(*manifest.Platform) {
				target = &manifest
				break
			}
		}
		if target == nil {
			return nil, fmt.Errorf("no match platform found in manifest list")
		} else {
			return fetchManifest(ctx, fetcher, *target)
		}
	default:
		return nil, fmt.Errorf("non manifest type digest fetched")
	}
}

func fetchConfig(ctx context.Context, fetcher remotes.Fetcher, desc specs.Descriptor) (*specs.Image, error) {
	config := specs.Image{}
	if err := fetch(ctx, fetcher, desc, &config); err != nil {
		return nil, fmt.Errorf("failed to fetch config: %w", err)
	}
	return &config, nil
}

func fetchManifestAndConfig(ctx context.Context, fetcher remotes.Fetcher, desc specs.Descriptor) (*specs.Manifest, *specs.Image, error) {
	var manifest *specs.Manifest
	var config *specs.Image
	manifest, err := fetchManifest(ctx, fetcher, desc)
	if err != nil {
		return nil, nil, fmt.Errorf("builder: failed to fetch manifest: %w", err)
	}

	config, err = fetchConfig(ctx, fetcher, manifest.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("builder: failed to fetch config: %w", err)
	}

	return manifest, config, nil
}

func downloadLayer(ctx context.Context, fetcher remotes.Fetcher, targetFile string, desc specs.Descriptor, decompress bool) error {
	rcoriginal, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return err
	}

	verifier := desc.Digest.Verifier()
	// tee the reader to verify the digest
	// this is because the decompression result
	// will be different from the original for which
	// the digest is calculated.
	rc := io.TeeReader(rcoriginal, verifier)

	dir := path.Dir(targetFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	ftar, err := os.Create(targetFile)
	if err != nil {
		return err
	}

	if decompress {
		rc, err = compression.DecompressStream(rc)
		if err != nil {
			return err
		}
	}
	if _, err = io.Copy(ftar, rc); err != nil {
		return err
	}

	if !verifier.Verified() {
		return fmt.Errorf("failed to verify digest %v", desc.Digest)
	}

	return nil
}

// TODO maybe refactor this
func writeConfig(dir string, configJSON *t.OverlayBDBSConfig) error {
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

func getFileDesc(filepath string, decompress bool) (specs.Descriptor, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return specs.Descriptor{}, err
	}
	defer file.Close()
	var rc io.ReadCloser
	if decompress {
		rc, err = compression.DecompressStream(file)
		if err != nil {
			return specs.Descriptor{}, err
		}
	} else {
		rc = file
	}

	h := sha256.New()
	size, err := io.Copy(h, rc)
	if err != nil {
		return specs.Descriptor{}, err
	}
	dgst := digest.NewDigest(digest.SHA256, h)
	return specs.Descriptor{
		Digest: dgst,
		Size:   size,
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
	return content.Copy(ctx, cw, bytes.NewReader(data), desc.Size, desc.Digest)
}

func tagPreviouslyConvertedManifest(ctx context.Context, pusher remotes.Pusher, fetcher remotes.Fetcher, desc specs.Descriptor) error {
	manifest := specs.Manifest{}
	if err := fetch(ctx, fetcher, desc, &manifest); err != nil {
		return fmt.Errorf("failed to fetch converted manifest: %w", err)
	}
	cbuf, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := uploadBytes(ctx, pusher, desc, cbuf); err != nil {
		return fmt.Errorf("failed to tag converted manifest: %w", err)
	}
	return nil
}

func buildArchiveFromFiles(ctx context.Context, target string, compress compression.Compression, files ...string) error {
	archive, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("failed to create tgz file: %q: %w", target, err)
	}
	defer archive.Close()
	fzip, err := compression.CompressStream(archive, compress)
	if err != nil {
		return fmt.Errorf("failed to create compression %v: %w", compress, err)
	}
	defer fzip.Close()
	ftar := tar.NewWriter(fzip)
	defer ftar.Close()
	for _, file := range files {
		if err := addFileToArchive(ctx, ftar, file); err != nil {
			return fmt.Errorf("failed to add file %q to archive %q: %w", file, target, err)
		}
	}
	return nil
}

func addFileToArchive(ctx context.Context, ftar *tar.Writer, filepath string) error {
	file, err := os.Open(filepath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("failed to open file: %q: %w", filepath, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return err
	}
	// remove timestamp for consistency
	if err = ftar.WriteHeader(&tar.Header{
		Name:     header.Name,
		Mode:     header.Mode,
		Size:     header.Size,
		Typeflag: header.Typeflag,
	}); err != nil {
		return err
	}
	_, err = io.Copy(ftar, file)
	if err != nil {
		return err
	}
	return nil
}
