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
	"encoding/json"
	"fmt"
	"testing"

	testingresources "github.com/containerd/accelerated-container-image/cmd/convertor/testingresources"
	"github.com/containerd/containerd/v2/core/remotes"
	_ "github.com/containerd/containerd/v2/pkg/testutil" // Handle custom root flag
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

func Test_builderEngineBase_isGzipLayer(t *testing.T) {
	ctx := context.Background()
	resolver := testingresources.GetTestResolver(t, ctx)

	type fields struct {
		fetcher  remotes.Fetcher
		manifest specs.Manifest
	}
	getFields := func(ctx context.Context, ref string) fields {
		_, desc, err := resolver.Resolve(ctx, ref)
		if err != nil {
			t.Error(err)
		}

		fetcher, err := resolver.Fetcher(ctx, ref)
		if err != nil {
			t.Error(err)
		}

		manifestStream, err := fetcher.Fetch(ctx, desc)
		if err != nil {
			t.Error(err)
		}

		if err != nil {
			t.Error(err)
		}

		parsedManifest := specs.Manifest{}
		decoder := json.NewDecoder(manifestStream)
		if err = decoder.Decode(&parsedManifest); err != nil {
			t.Error(err)
		}

		return fields{
			fetcher:  fetcher,
			manifest: parsedManifest,
		}
	}

	type args struct {
		ctx context.Context
		idx int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    bool
		wantErr bool
	}{
		// TODO Add more layers types for validation
		// Unknown Layer Type
		// Uncompressed Layer Type
		{
			name:   "Valid Gzip Layer",
			fields: getFields(ctx, testingresources.DockerV2_Manifest_Simple_Ref),
			args: args{
				ctx: ctx,
				idx: 0,
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "Layer Not Found",
			fields: func() fields {
				fields := getFields(ctx, testingresources.DockerV2_Manifest_Simple_Ref)
				fields.manifest.Layers[0].Digest = digest.FromString("sample")
				return fields
			}(),
			args: args{
				ctx: ctx,
				idx: 0,
			},
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &builderEngineBase{
				fetcher:  tt.fields.fetcher,
				manifest: tt.fields.manifest,
			}
			got, err := e.isGzipLayer(tt.args.ctx, tt.args.idx)
			if (err != nil) != tt.wantErr {
				t.Errorf("builderEngineBase.isGzipLayer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("builderEngineBase.isGzipLayer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getBuilderEngineBase(t *testing.T) {
	resolver := testingresources.GetTestResolver(t, context.Background())
	engine, err := getBuilderEngineBase(context.TODO(),
		resolver,
		testingresources.DockerV2_Manifest_Simple_Ref,
		fmt.Sprintf("%s-obd", testingresources.DockerV2_Manifest_Simple_Ref),
	)
	if err != nil {
		t.Error(err)
	}

	testingresources.Assert(t, engine.fetcher != nil, "Fetcher is nil")
	testingresources.Assert(t, engine.pusher != nil, "Pusher is nil")
	testingresources.Assert(t,
		engine.manifest.Config.Digest == testingresources.DockerV2_Manifest_Simple_Config_Digest,
		fmt.Sprintf("Config Digest is not equal to %s", testingresources.DockerV2_Manifest_Simple_Config_Digest))

	content, err := testingresources.ConsistentManifestMarshal(&engine.manifest)
	if err != nil {
		t.Errorf("Could not parse obtained manifest, got: %v", err)
	}

	testingresources.Assert(t,
		digest.FromBytes(content) == testingresources.DockerV2_Manifest_Simple_Digest,
		fmt.Sprintf("Manifest Digest is not equal to %s", testingresources.DockerV2_Manifest_Simple_Digest))
}

func Test_uploadManifestAndConfig(t *testing.T) {
}
