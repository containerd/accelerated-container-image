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
	"github.com/containerd/containerd/errdefs"
	_ "github.com/containerd/containerd/pkg/testutil" // Handle custom root flag
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

	err := base.db.CreateEntry(ctx, e.host, e.repository, targetDesc.Digest, fakeChainId, targetDesc.Size)
	if err != nil {
		t.Error(err)
	}

	t.Run("Entry in DB and in Registry", func(t *testing.T) {
		desc, err := e.CheckForConvertedLayer(ctx, 0)

		if err != nil {
			t.Error(err)
		}

		testingresources.Assert(t, desc.Size == targetDesc.Size, "CheckForConvertedLayer() returned improper size layer")
		testingresources.Assert(t, desc.Digest == targetDesc.Digest, "CheckForConvertedLayer() returned incorrect digest")
	})

	base.db = testingresources.NewLocalDB() // Reset DB
	digestNotInRegistry := digest.FromString("Not in reg")
	err = base.db.CreateEntry(ctx, e.host, e.repository, digestNotInRegistry, fakeChainId, 10)
	if err != nil {
		t.Error(err)
	}

	t.Run("Entry in DB but not in registry", func(t *testing.T) {
		_, err := e.CheckForConvertedLayer(ctx, 0)
		testingresources.Assert(t, errdefs.IsNotFound(err), fmt.Sprintf("CheckForConvertedLayer() returned an unexpected Error: %v", err))
		entry := base.db.GetEntryForRepo(ctx, e.host, e.repository, fakeChainId)
		testingresources.Assert(t, entry == nil, "CheckForConvertedLayer() Invalid entry was not cleaned up")
	})
	// TODO: Cross Repo Mount Scenario
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
