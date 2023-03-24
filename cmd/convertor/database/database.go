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
	GetEntryForRepo(ctx context.Context, host string, repository string, chainID string) *Entry
	GetCrossRepoEntries(ctx context.Context, host string, chainID string) []*Entry
	CreateEntry(ctx context.Context, host string, repository string, convertedDigest digest.Digest, chainID string, size int64) error
	DeleteEntry(ctx context.Context, host string, repository string, chainID string) error
}

type Entry struct {
	ConvertedDigest digest.Digest
	DataSize        int64
	Repository      string
	ChainID         string
	Host            string
}
