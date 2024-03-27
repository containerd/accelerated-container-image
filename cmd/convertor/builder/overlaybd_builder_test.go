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
	"context"
	"fmt"
	"testing"

	testingresources "github.com/containerd/accelerated-container-image/cmd/convertor/testingresources"
	"github.com/containerd/accelerated-container-image/pkg/version"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	_ "github.com/containerd/containerd/pkg/testutil" // Handle custom root flag
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func Test_overlaybd_builder_CheckForConvertedLayer(t *testing.T) {
	ctx := context.Background()
	versionDB := testingresources.Localdb{
		Version: version.GetUserSpaceConsistencyVersion(),
	}
	db := &versionDB // Reset DB
	resolver := testingresources.GetTestResolver(t, ctx)
	fetcher := testingresources.GetTestFetcherFromResolver(t, ctx, resolver, testingresources.DockerV2_Manifest_Simple_Ref)
	base := &builderEngineBase{
		fetcher:    fetcher,
		host:       "sample.localstore.io",
		repository: "hello-world",
	}

	// TODO: Maybe change this for an actually converted layer in the future
	targetDesc := v1.Descriptor{
		Digest:    testingresources.DockerV2_Manifest_Simple_Layer_0_Digest,
		Size:      testingresources.DockerV2_Manifest_Simple_Layer_0_Size,
		MediaType: v1.MediaTypeImageLayerGzip,
	}

	fakeChainId := "fake-chain-id" // We don't validate the chainID itself so such values are fine for testing
	e := &overlaybdBuilderEngine{
		builderEngineBase: base,
		overlaybdLayers: []overlaybdConvertResult{
			{
				chainID: fakeChainId,
			},
		},
	}

	t.Run("No DB Present", func(t *testing.T) {
		_, err := e.CheckForConvertedLayer(ctx, 0)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedLayer() returned an unexpected Error: %v", err))
	})

	base.db = db
	t.Run("No Entry in DB", func(t *testing.T) {
		_, err := e.CheckForConvertedLayer(ctx, 0)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedLayer() returned an unexpected Error: %v", err))
	})

	err := base.db.CreateLayerEntry(ctx, e.host, e.repository, targetDesc.Digest, fakeChainId, targetDesc.Size)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Layer entry in DB and in Registry", func(t *testing.T) {
		desc, err := e.CheckForConvertedLayer(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}

		testingresources.Assert(t, desc.Size == targetDesc.Size, "CheckForConvertedLayer() returned improper size layer")
		testingresources.Assert(t, desc.Digest == targetDesc.Digest, "CheckForConvertedLayer() returned incorrect digest")
	})

	versionDB.Version.LayerVersion = "A" // Change version to something else
	t.Run("Entry in DB but wrong version (should return not found)", func(t *testing.T) {
		_, err := e.CheckForConvertedLayer(ctx, 0)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedLayer() returned an unexpected Error: %v", err))
	})
	versionDB.Version = version.GetUserSpaceConsistencyVersion() // Reset version

	// cross repo mount (change target repo)
	base.repository = "hello-world2"
	newImageRef := "sample.localstore.io/hello-world2:amd64"
	e.resolver = testingresources.GetTestResolver(t, ctx)
	e.pusher = testingresources.GetTestPusherFromResolver(t, ctx, e.resolver, newImageRef)

	t.Run("Cross repo layer entry found in DB mount", func(t *testing.T) {
		desc, err := e.CheckForConvertedLayer(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}

		testingresources.Assert(t, desc.Size == targetDesc.Size, "CheckForConvertedLayer() returned improper size layer")
		testingresources.Assert(t, desc.Digest == targetDesc.Digest, "CheckForConvertedLayer() returned incorrect digest")

		// check that the images can be pulled from the mounted repo
		fetcher := testingresources.GetTestFetcherFromResolver(t, ctx, e.resolver, newImageRef)
		rc, err := fetcher.Fetch(ctx, desc)
		if err != nil {
			t.Fatal(err)
		}
		rc.Close()
	})

	versionDB.Version.LayerVersion = "A" // Change version to something else
	t.Run("Cross Repo Entry in DB but wrong version (should return not found)", func(t *testing.T) {
		_, err := e.CheckForConvertedLayer(ctx, 0)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedLayer() returned an unexpected Error: %v", err))
	})

	base.db = testingresources.NewLocalDB(version.GetUserSpaceConsistencyVersion()) // Reset DB
	digestNotInRegistry := digest.FromString("Not in reg")
	err = base.db.CreateLayerEntry(ctx, e.host, e.repository, digestNotInRegistry, fakeChainId, 10)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Entry in DB but not in registry", func(t *testing.T) {
		_, err := e.CheckForConvertedLayer(ctx, 0)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedLayer() returned an unexpected Error: %v", err))
		entry := base.db.GetLayerEntryForRepo(ctx, e.host, e.repository, fakeChainId)
		testingresources.Assert(t, entry == nil, "CheckForConvertedLayer() Invalid entry was not cleaned up")
	})
}

func Test_overlaybd_builder_CheckForConvertedManifest(t *testing.T) {
	ctx := context.Background()
	versionDB := testingresources.Localdb{
		Version: version.GetUserSpaceConsistencyVersion(),
	}
	db := &versionDB
	resolver := testingresources.GetTestResolver(t, ctx)
	fetcher := testingresources.GetTestFetcherFromResolver(t, ctx, resolver, testingresources.DockerV2_Manifest_Simple_Ref)

	// Unconverted hello world-image
	inputDesc := v1.Descriptor{
		MediaType: images.MediaTypeDockerSchema2Manifest,
		Digest:    testingresources.DockerV2_Manifest_Simple_Digest,
		Size:      testingresources.DockerV2_Manifest_Simple_Size,
	}

	// Converted hello world-image
	outputDesc := v1.Descriptor{
		MediaType: v1.MediaTypeImageManifest,
		Digest:    testingresources.DockerV2_Manifest_Simple_Converted_Digest,
		Size:      testingresources.DockerV2_Manifest_Simple_Converted_Size,
	}

	base := &builderEngineBase{
		fetcher:    fetcher,
		host:       "sample.localstore.io",
		repository: "hello-world",
		inputDesc:  inputDesc,
		resolver:   resolver,
		oci:        true,
	}

	e := &overlaybdBuilderEngine{
		builderEngineBase: base,
	}

	t.Run("No DB Present", func(t *testing.T) {
		_, err := e.CheckForConvertedManifest(ctx)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedManifest() returned an unexpected Error: %v", err))
	})

	base.db = db

	// Store a fake converted manifest in the DB
	err := base.db.CreateManifestEntry(ctx, e.host, e.repository, outputDesc.MediaType, inputDesc.Digest, outputDesc.Digest, outputDesc.Size)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Entry in DB and in Registry", func(t *testing.T) {
		desc, err := e.CheckForConvertedManifest(ctx)
		if err != nil {
			t.Fatal(err)
		}

		testingresources.Assert(t, desc.Size == outputDesc.Size, "CheckForConvertedManifest() returned incorrect size")
		testingresources.Assert(t, desc.Digest == outputDesc.Digest, "CheckForConvertedManifest() returned incorrect digest")
	})

	versionDB.Version.ManifestVersion = "A" // Change version to something else
	t.Run("Entry in DB but wrong version (should return not found)", func(t *testing.T) {
		_, err := e.CheckForConvertedManifest(ctx)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedManifest() returned an unexpected Error: %v", err))
	})
	versionDB.Version = version.GetUserSpaceConsistencyVersion() // Reset version

	// cross repo mount (change target repo)
	base.repository = "hello-world2"
	newImageRef := "sample.localstore.io/hello-world2:amd64"
	e.resolver = testingresources.GetTestResolver(t, ctx)
	e.pusher = testingresources.GetTestPusherFromResolver(t, ctx, e.resolver, newImageRef)

	t.Run("Cross Repo Entry found in DB mount", func(t *testing.T) {
		_, err := e.CheckForConvertedManifest(ctx)
		testingresources.Assert(t, err == nil, fmt.Sprintf("CheckForConvertedManifest() returned an unexpected Error: %v", err))
		// check that the images can be pulled from the mounted repo
		fetcher := testingresources.GetTestFetcherFromResolver(t, ctx, e.resolver, newImageRef)
		_, desc, err := e.resolver.Resolve(ctx, newImageRef)
		if err != nil {
			t.Fatal(err)
		}
		manifest, config, err := fetchManifestAndConfig(ctx, fetcher, desc)
		if err != nil {
			t.Fatal(err)
		}
		if manifest == nil || config == nil {
			t.Fatalf("Could not pull mounted manifest or config")
		}
		rc, err := fetcher.Fetch(ctx, manifest.Layers[0])
		if err != nil {
			t.Fatal(err)
		}
		rc.Close()
	})

	versionDB.Version.ManifestVersion = "A" // Change version to something else
	t.Run("Cross Repo Entry in DB but wrong version (should return not found)", func(t *testing.T) {
		_, err := e.CheckForConvertedManifest(ctx)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedManifest() returned an unexpected Error: %v", err))
	})

	base.db = testingresources.NewLocalDB(version.GetUserSpaceConsistencyVersion())
	digestNotInRegistry := digest.FromString("Not in reg")
	err = base.db.CreateManifestEntry(ctx, e.host, e.repository, outputDesc.MediaType, inputDesc.Digest, digestNotInRegistry, outputDesc.Size)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Entry in DB but not in registry", func(t *testing.T) {
		_, err := e.CheckForConvertedManifest(ctx)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedManifest() returned an unexpected Error: %v", err))
		entry := base.db.GetManifestEntryForRepo(ctx, e.host, e.repository, outputDesc.MediaType, inputDesc.Digest)
		testingresources.Assert(t, entry == nil, "CheckForConvertedManifest() Invalid entry was not cleaned up")
	})
}

func Test_overlaybd_builder_StoreConvertedLayerDetails(t *testing.T) {
	ctx := context.Background()
	base := &builderEngineBase{
		db: nil,
	}
	e := &overlaybdBuilderEngine{
		builderEngineBase: base,
	}
	// No DB Case
	err := e.StoreConvertedLayerDetails(ctx, 0)
	testingresources.Assert(t, err == nil, "StoreConvertedLayerDetails() returned an unexpected Error")
}
