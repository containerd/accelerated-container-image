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

package convert_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	dockerspec "github.com/containerd/containerd/v2/core/images"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	ArtifactTypeOverlaybd = "application/vnd.containerd.overlaybd.native.v1+json"
	ArtifactTypeTurboOCI  = "application/vnd.containerd.overlaybd.turbo.v1+json"

	TestRepository = "localhost:5000/hello-world"
)

func TestConvertReferrer(t *testing.T) {
	ctx := context.Background()
	require := require.New(t)

	if err := copyImageToRegistry(ctx); err != nil {
		t.Fatal(err)
	}

	repo, err := remote.NewRepository(TestRepository)
	require.Nil(err, err)
	repo.SetReferrersCapability(true)

	fetchJSON := func(src ocispec.Descriptor, v any) {
		rc, err := repo.Fetch(ctx, src)
		require.Nil(err, err)
		defer rc.Close()
		bytes, err := io.ReadAll(rc)
		require.Nil(err, err)
		err = json.Unmarshal(bytes, v)
		require.Nil(err, err)

		t.Logf("fetch %s, content:\n%s", src.Digest.String(), string(bytes))
	}

	var checkReferrer func(src, dst ocispec.Descriptor, artifactType string)
	checkReferrer = func(src, dst ocispec.Descriptor, artifactType string) {
		t.Logf("checking src %v to dst %v, artifact type %s", src, dst, artifactType)
		require.Equal(isIndex(src.MediaType), isIndex(dst.MediaType))

		switch dst.MediaType {
		case ocispec.MediaTypeImageManifest, dockerspec.MediaTypeDockerSchema2Manifest:
			var manifest ocispec.Manifest
			fetchJSON(dst, &manifest)
			require.Equal(artifactType, manifest.ArtifactType)
			require.Equal(src.Digest, manifest.Subject.Digest)
			require.Equal(src.Size, manifest.Subject.Size)
			require.Equal(src.MediaType, manifest.Subject.MediaType)

		case ocispec.MediaTypeImageIndex, dockerspec.MediaTypeDockerSchema2ManifestList:
			var index ocispec.Index
			fetchJSON(dst, &index)
			require.Equal(artifactType, index.ArtifactType)
			require.Equal(src.Digest, index.Subject.Digest)
			require.Equal(src.Size, index.Subject.Size)
			require.Equal(src.MediaType, index.Subject.MediaType)

			var srcIndex ocispec.Index
			fetchJSON(src, &srcIndex)
			require.Equal(len(srcIndex.Manifests), len(index.Manifests))
			for idx := range index.Manifests {
				checkReferrer(srcIndex.Manifests[idx], index.Manifests[idx], artifactType)
			}
		}
	}

	testcases := []struct {
		name      string
		reference string
	}{
		{
			name:      "simple",
			reference: TestRepository + ":amd64",
		},
		{
			name:      "index",
			reference: TestRepository + ":multi-arch",
		},
		{
			name:      "nested index",
			reference: TestRepository + ":nested-index",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			err := convert(ctx, tc.reference)
			require.Nil(err, err)

			src, err := repo.Resolve(ctx, tc.reference)
			require.Nil(err, err)

			obd, err := repo.Resolve(ctx, tc.reference+"_overlaybd")
			require.Nil(err, err)

			turbo, err := repo.Resolve(ctx, tc.reference+"_turbo")
			require.Nil(err, err)

			checkReferrer(src, obd, ArtifactTypeOverlaybd)
			checkReferrer(src, turbo, ArtifactTypeTurboOCI)
		})
	}
}

const convertBin = "/opt/overlaybd/snapshotter/convertor"

func convert(ctx context.Context, refspec string) error {
	ref, err := registry.ParseReference(refspec)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, convertBin,
		"-r", fmt.Sprintf("%s/%s", ref.Registry, ref.Repository),
		"-i", ref.Reference,
		"--overlaybd", ref.Reference+"_overlaybd",
		"--turboOCI", ref.Reference+"_turbo",
		"--referrer",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to convert: %w, output: %s", err, out)
	}
	return nil
}

func copyImageToRegistry(ctx context.Context) error {
	_, filename, _, _ := runtime.Caller(0)
	local, err := oci.New(filepath.Join(filepath.Dir(filename), "..", "resources", "store", "hello-world"))
	if err != nil {
		return err
	}
	repo, err := remote.NewRepository(TestRepository)
	if err != nil {
		return err
	}

	images := []string{"amd64", "arm64", "multi-arch", "nested-index"}
	for _, img := range images {
		src := img
		dst := TestRepository + ":" + img
		if _, err := oras.Copy(ctx, local, src, repo, dst, oras.CopyOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func isIndex(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageIndex || mediaType == dockerspec.MediaTypeDockerSchema2ManifestList
}
