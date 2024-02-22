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

package database

import (
	"context"

	"github.com/opencontainers/go-digest"
)

type ConversionDatabase interface {
	// Layer Entries
	CreateLayerEntry(ctx context.Context, host, repository string, convertedDigest digest.Digest, chainID string, size int64) error
	GetLayerEntryForRepo(ctx context.Context, host, repository, chainID string) *LayerEntry
	GetCrossRepoLayerEntries(ctx context.Context, host, chainID string) []*LayerEntry
	DeleteLayerEntry(ctx context.Context, host, repository, chainID string) error

	// Manifest Entries
	CreateManifestEntry(ctx context.Context, host, repository, mediatype string, original, convertedDigest digest.Digest, size int64) error
	GetManifestEntryForRepo(ctx context.Context, host, repository, mediatype string, original digest.Digest) *ManifestEntry
	GetCrossRepoManifestEntries(ctx context.Context, host, mediatype string, original digest.Digest) []*ManifestEntry
	DeleteManifestEntry(ctx context.Context, host, repository, mediatype string, original digest.Digest) error
}

type LayerEntry struct {
	ConvertedDigest digest.Digest
	DataSize        int64
	Repository      string
	ChainID         string
	Host            string
}

type ManifestEntry struct {
	ConvertedDigest digest.Digest
	OriginalDigest  digest.Digest
	DataSize        int64
	Repository      string
	Host            string
	MediaType       string
}
