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
	"sync"

	"github.com/containerd/accelerated-container-image/cmd/convertor/database"
	"github.com/opencontainers/go-digest"
)

type localdb struct {
	layerRecords    []*database.LayerEntry
	manifestRecords []*database.ManifestEntry
	layerLock       sync.Mutex // Protects layerRecords
	manifestLock    sync.Mutex // Protects manifestRecords
}

// NewLocalDB returns a new local database for testing. This is a simple unoptimized in-memory database.
func NewLocalDB() database.ConversionDatabase {
	return &localdb{}
}

func (l *localdb) CreateLayerEntry(ctx context.Context, host string, repository string, convertedDigest digest.Digest, chainID string, size int64) error {
	l.layerLock.Lock()
	defer l.layerLock.Unlock()
	l.layerRecords = append(l.layerRecords, &database.LayerEntry{
		Host:            host,
		Repository:      repository,
		ChainID:         chainID,
		ConvertedDigest: convertedDigest,
		DataSize:        size,
	})
	return nil
}

func (l *localdb) GetLayerEntryForRepo(ctx context.Context, host string, repository string, chainID string) *database.LayerEntry {
	l.layerLock.Lock()
	defer l.layerLock.Unlock()
	for _, entry := range l.layerRecords {
		if entry.Host == host && entry.ChainID == chainID && entry.Repository == repository {
			return entry
		}
	}
	return nil
}

func (l *localdb) GetCrossRepoLayerEntries(ctx context.Context, host, chainID string) []*database.LayerEntry {
	l.layerLock.Lock()
	defer l.layerLock.Unlock()
	var entries []*database.LayerEntry
	for _, entry := range l.layerRecords {
		if entry.Host == host && entry.ChainID == chainID {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (l *localdb) DeleteLayerEntry(ctx context.Context, host, repository, chainID string) error {
	l.layerLock.Lock()
	defer l.layerLock.Unlock()
	// host - repo - chainID should be unique
	for i, entry := range l.layerRecords {
		if entry.Host == host && entry.ChainID == chainID && entry.Repository == repository {
			l.layerRecords = append(l.layerRecords[:i], l.layerRecords[i+1:]...)
			return nil
		}
	}
	return nil // No error if entry not found
}

func (l *localdb) CreateManifestEntry(ctx context.Context, host, repository, mediaType string, original, convertedDigest digest.Digest, size int64) error {
	l.manifestLock.Lock()
	defer l.manifestLock.Unlock()
	l.manifestRecords = append(l.manifestRecords, &database.ManifestEntry{
		Host:            host,
		Repository:      repository,
		OriginalDigest:  original,
		ConvertedDigest: convertedDigest,
		DataSize:        size,
		MediaType:       mediaType,
	})
	return nil
}

func (l *localdb) GetManifestEntryForRepo(ctx context.Context, host, repository, mediaType string, original digest.Digest) *database.ManifestEntry {
	l.manifestLock.Lock()
	defer l.manifestLock.Unlock()
	for _, entry := range l.manifestRecords {
		if entry.Host == host && entry.OriginalDigest == original && entry.Repository == repository && entry.MediaType == mediaType {
			return entry
		}
	}
	return nil
}

func (l *localdb) GetCrossRepoManifestEntries(ctx context.Context, host, mediaType string, original digest.Digest) []*database.ManifestEntry {
	l.manifestLock.Lock()
	defer l.manifestLock.Unlock()
	var entries []*database.ManifestEntry
	for _, entry := range l.manifestRecords {
		if entry.Host == host && entry.OriginalDigest == original && entry.MediaType == mediaType {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (l *localdb) DeleteManifestEntry(ctx context.Context, host, repository, mediaType string, original digest.Digest) error {
	l.manifestLock.Lock()
	defer l.manifestLock.Unlock()
	// Identify indices of items to be deleted.
	for i, entry := range l.manifestRecords {
		if entry.Host == host && entry.OriginalDigest == original && entry.Repository == repository && entry.MediaType == mediaType {
			l.manifestRecords = append(l.manifestRecords[:i], l.manifestRecords[i+1:]...)
		}
	}
	return nil // No error if entry not found
}
