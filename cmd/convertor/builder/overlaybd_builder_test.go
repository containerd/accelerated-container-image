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
	"os"
	"path"
	"path/filepath"
	"testing"

	testingresources "github.com/containerd/accelerated-container-image/cmd/convertor/testingresources"
	sn "github.com/containerd/accelerated-container-image/pkg/types"
	"github.com/containerd/errdefs"

	"github.com/containerd/containerd/v2/core/images"
	_ "github.com/containerd/containerd/v2/pkg/testutil" // Handle custom root flag
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func Test_overlaybd_builder_CheckForConvertedLayer(t *testing.T) {
	ctx := context.Background()
	db := testingresources.NewLocalDB()
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

	base.db = testingresources.NewLocalDB() // Reset DB
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
	db := testingresources.NewLocalDB()
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

	base.db = testingresources.NewLocalDB() // Reset DB
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
		db:         nil,
		repository: "hello-world",
		host:       "sample.localstore.io",
	}

	e := &overlaybdBuilderEngine{
		builderEngineBase: base,
		overlaybdLayers: []overlaybdConvertResult{
			{
				fromDedup: true,
				// TODO: Maybe change this for an actually converted layer in the future
				desc: v1.Descriptor{
					Digest:    testingresources.DockerV2_Manifest_Simple_Layer_0_Digest,
					Size:      testingresources.DockerV2_Manifest_Simple_Layer_0_Size,
					MediaType: v1.MediaTypeImageLayerGzip,
				},
				chainID: "fake-chain-id",
			},
		},
	}
	e.manifest = v1.Manifest{
		Layers: []v1.Descriptor{
			{
				Digest:    testingresources.DockerV2_Manifest_Simple_Layer_0_Digest,
				Size:      testingresources.DockerV2_Manifest_Simple_Layer_0_Size,
				MediaType: v1.MediaTypeImageLayerGzip,
			},
		},
	}
	t.Run("No DB Present", func(t *testing.T) {
		err := e.StoreConvertedLayerDetails(ctx, 0)
		testingresources.Assert(t, err == nil, "StoreConvertedLayerDetails() returned an unexpected Error")
	})

	t.Run("Layer is marked as deduplicated, avoid storing", func(t *testing.T) {
		base.db = testingresources.NewLocalDB()

		err := e.StoreConvertedLayerDetails(ctx, 0)
		testingresources.Assert(t, err == nil, "StoreConvertedLayerDetails() returned an unexpected Error")
		base.db.GetLayerEntryForRepo(ctx, e.host, e.repository, "fake-chain-id")
	})
}

func Test_overlaybd_builder_BuildLayer_HandlesPreviouslyConvertedLayers(t *testing.T) {
	ctx := context.Background()
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
	config := &sn.OverlayBDBSConfig{
		Lowers:     []sn.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	config.Lowers = append(config.Lowers, sn.OverlayBDBSConfigLower{
		File: overlaybdBaseLayer,
	})

	t.Run("Dedup working as expected", func(t *testing.T) {
		e := &overlaybdBuilderEngine{
			builderEngineBase: base,
			overlaybdConfig:   config,
		}
		e.manifest.Layers = []v1.Descriptor{targetDesc}
		tmpDir := t.TempDir()
		e.workDir = tmpDir
		e.overlaybdLayers = []overlaybdConvertResult{
			{
				fromDedup: true,
			},
		}

		// Try before setting the commit file. This will fail because create tool
		// is not present but helps verify the fallback. Error type here depends
		// on if the tool is present or not.
		if err := e.BuildLayer(ctx, 0); err == nil {
			t.Fatal("Expected an error but got none")
		}

		// Simulate a commit file
		if err := os.MkdirAll(e.getLayerDir(0), 0777); err != nil {
			t.Fatal(err)
		}

		// Try again with parent directory present
		if err := e.BuildLayer(ctx, 0); err == nil {
			t.Fatal("Expected an error but got none")
		}

		file, err := os.Create(filepath.Join(e.getLayerDir(0), commitFile))
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		if err = e.BuildLayer(ctx, 0); err != nil {
			t.Error(err)
		}
	})

	// We attempt to clean any leftover files when we fail to download a dedup
	// layer so this scenario is unlikely to happen but it's a good sanity check.
	// In this case, we assume the commit file is present but invalid.
	t.Run("Dedup left partial commit file", func(t *testing.T) {
		e := &overlaybdBuilderEngine{
			builderEngineBase: base,
			overlaybdConfig:   config,
		}
		e.manifest.Layers = []v1.Descriptor{targetDesc}
		tmpDir := t.TempDir()
		e.workDir = tmpDir
		e.overlaybdLayers = []overlaybdConvertResult{
			{
				fromDedup: false,
			},
		}
		// Simulate a commit file
		if err := os.MkdirAll(e.getLayerDir(0), 0777); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(e.getLayerDir(0), commitFile))
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		if err = e.BuildLayer(ctx, 0); err == nil {
			t.Fatal("Expected an error but got none")
		} else {
			if err.Error() != "layer 0 is not from dedup but commit file is present" {
				t.Fatalf("Unexpected error: %v", err)
			}
		}
	})

	// This is a scenario that should not happen but it's a good sanity check.
	t.Run("Dedup but somehow failed to download commit file", func(t *testing.T) {
		e := &overlaybdBuilderEngine{
			builderEngineBase: base,
			overlaybdConfig:   config,
		}
		e.manifest.Layers = []v1.Descriptor{targetDesc}
		tmpDir := t.TempDir()
		e.workDir = tmpDir
		e.overlaybdLayers = []overlaybdConvertResult{
			{
				fromDedup: true,
			},
		}
		// Simulate a commit file
		if err := os.MkdirAll(e.getLayerDir(0), 0777); err != nil {
			t.Fatal(err)
		}

		if err := e.BuildLayer(ctx, 0); err == nil {
			t.Fatal("Expected an error but got none")
		} else {
			if err.Error() != "layer 0 is from dedup but commit file is missing" {
				t.Fatalf("Unexpected error: %v", err)
			}
		}
	})
}

func Test_overlaybd_builder_DownloadConvertedLayer(t *testing.T) {
	ctx := context.Background()
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
	config := &sn.OverlayBDBSConfig{
		Lowers:     []sn.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	config.Lowers = append(config.Lowers, sn.OverlayBDBSConfigLower{
		File: overlaybdBaseLayer,
	})

	t.Run("DownloadConvertedLayer Succeeds", func(t *testing.T) {
		e := &overlaybdBuilderEngine{
			builderEngineBase: base,
			overlaybdConfig:   config,
		}
		e.manifest.Layers = []v1.Descriptor{targetDesc}
		tmpDir := t.TempDir()
		e.workDir = tmpDir
		e.overlaybdLayers = []overlaybdConvertResult{
			{
				fromDedup: false,
			},
		}

		if err := e.DownloadConvertedLayer(ctx, 0, targetDesc); err != nil {
			t.Fatalf("DownloadConvertedLayer() failed with error: %v", err)
		}

		// Check if the commit file is present
		if _, err := os.Stat(filepath.Join(e.getLayerDir(0), commitFile)); err != nil {
			t.Fatalf("Expected commit file but got: %v", err)
		}

		testingresources.Assert(t, e.overlaybdLayers[0].fromDedup, "DownloadConvertedLayer() did not mark layer as dedup")
		testingresources.Assert(t, e.overlaybdLayers[0].desc.Digest != "", "DownloadConvertedLayer() did not set the digest")
	})
}

func Test_overlaybd_builder_UploadLayer(t *testing.T) {
	ctx := context.Background()
	targetManifest := "sample.localstore.io/hello-world:another"
	resolver := testingresources.GetTestResolver(t, ctx)
	fetcher := testingresources.GetTestFetcherFromResolver(t, ctx, resolver, testingresources.DockerV2_Manifest_Simple_Ref)
	pusher := testingresources.GetTestPusherFromResolver(t, ctx, resolver, targetManifest)
	base := &builderEngineBase{
		fetcher:    fetcher,
		host:       "sample.localstore.io",
		repository: "hello-world",
		pusher:     pusher,
	}
	// TODO: Maybe change this for an actually converted layer in the future
	targetDesc := v1.Descriptor{
		Digest:    testingresources.DockerV2_Manifest_Simple_Layer_0_Digest,
		Size:      testingresources.DockerV2_Manifest_Simple_Layer_0_Size,
		MediaType: v1.MediaTypeImageLayerGzip,
	}
	config := &sn.OverlayBDBSConfig{
		Lowers:     []sn.OverlayBDBSConfigLower{},
		ResultFile: "",
	}
	config.Lowers = append(config.Lowers, sn.OverlayBDBSConfigLower{
		File: overlaybdBaseLayer,
	})

	t.Run("UploadLayer Succeeds for non dedup layer", func(t *testing.T) {
		e := &overlaybdBuilderEngine{
			builderEngineBase: base,
			overlaybdConfig:   config,
		}
		e.manifest.Layers = []v1.Descriptor{targetDesc}
		tmpDir := t.TempDir()
		e.workDir = tmpDir
		e.overlaybdLayers = []overlaybdConvertResult{
			{
				fromDedup: false,
			},
		}
		// Get a commit file (We are using the downloadConvertedLayer here just to get the file)
		if err := e.DownloadConvertedLayer(ctx, 0, targetDesc); err != nil {
			t.Fatalf("DownloadConvertedLayer() failed with error: %v", err)
		}
		e.overlaybdLayers[0].fromDedup = false // Reset the flag to simulate a non dedup layer

		if err := e.UploadLayer(ctx, 0); err != nil {
			t.Fatalf("UploadLayer() failed with error: %v", err)
		}
	})

	t.Run("UploadLayer Fails for non matching dedup layer", func(t *testing.T) {
		e := &overlaybdBuilderEngine{
			builderEngineBase: base,
			overlaybdConfig:   config,
		}
		e.manifest.Layers = []v1.Descriptor{targetDesc}
		tmpDir := t.TempDir()
		e.workDir = tmpDir
		e.overlaybdLayers = []overlaybdConvertResult{
			{
				desc:      targetDesc, // Set the desc to the targetDesc to simulate a mismatch
				fromDedup: true,
			},
		}
		// Simulate a corrupted commit file
		if err := os.MkdirAll(e.getLayerDir(0), 0777); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(e.getLayerDir(0), commitFile))
		if err != nil {
			t.Fatal(err)
		}
		file.WriteString("corrupted overlaybdfile simulation")
		file.Close()

		layerDir := e.getLayerDir(0)
		corruptedDesc, err := getFileDesc(path.Join(layerDir, commitFile), false)
		if err != nil {
			t.Fatalf("getFileDesc() failed with error: %v", err)
		}

		// Upload should fail because the converted layer is not as expected
		if err = e.UploadLayer(ctx, 0); err != nil {
			if err.Error() != fmt.Sprintf("layer %d digest mismatch, expected %s, got %s", 0, targetDesc.Digest, corruptedDesc.Digest) {
				t.Fatalf("UploadLayer() failed with unexpected error: %v", err)
			}
		}
	})
}
