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

package testingresources

import (
	"context"

	"github.com/containerd/accelerated-container-image/cmd/convertor/database"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type localdb struct {
	records []*database.Entry
}

// NewLocalDB returns a new local database for testing. This is a simple unoptimized in-memory database.
func NewLocalDB() database.ConversionDatabase {
	return &localdb{}
}

func (l *localdb) GetEntryForRepo(ctx context.Context, host string, repository string, chainID string) *database.Entry {
	for _, entry := range l.records {
		if entry.Host == host && entry.ChainID == chainID && entry.Repository == repository {
			return entry
		}
	}
	return nil
}

func (l *localdb) GetCrossRepoEntries(ctx context.Context, host string, chainID string) []*database.Entry {
	var entries []*database.Entry
	for _, entry := range l.records {
		if entry.Host == host && entry.ChainID == chainID {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (l *localdb) CreateEntry(ctx context.Context, host string, repository string, convertedDigest digest.Digest, chainID string, size int64) error {
	l.records = append(l.records, &database.Entry{
		Host:            host,
		Repository:      repository,
		ChainID:         chainID,
		ConvertedDigest: convertedDigest,
		DataSize:        size,
	})
	return nil
}

func (l *localdb) DeleteEntry(ctx context.Context, host string, repository string, chainID string) error {
	// Identify indices of items to be deleted.
	var indicesToDelete []int
	for i, entry := range l.records {
		if entry.Host == host && entry.ChainID == chainID && entry.Repository == repository {
			indicesToDelete = append(indicesToDelete, i)
		}
	}

	if len(indicesToDelete) == 0 {
		return errors.Errorf("failed to find entry for host %s, repository %s, chainID %s", host, repository, chainID)
	}

	// Delete items at identified indices. (Reverse order to avoid index shifting.)
	for i := len(indicesToDelete) - 1; i >= 0; i-- {
		l.records = append(l.records[:indicesToDelete[i]], l.records[indicesToDelete[i]+1:]...)
	}
	return nil
}
