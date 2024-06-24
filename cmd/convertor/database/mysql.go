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
	"database/sql"
	"fmt"

	"github.com/containerd/log"
	"github.com/opencontainers/go-digest"
)

type sqldb struct {
	db *sql.DB
}

func NewSqlDB(db *sql.DB) ConversionDatabase {
	return &sqldb{
		db: db,
	}
}

func (m *sqldb) CreateLayerEntry(ctx context.Context, host, repository string, convertedDigest digest.Digest, chainID string, size int64) error {
	_, err := m.db.ExecContext(ctx, "insert into overlaybd_layers(host, repo, chain_id, data_digest, data_size) values(?, ?, ?, ?, ?)", host, repository, chainID, convertedDigest, size)
	return err
}

func (m *sqldb) GetLayerEntryForRepo(ctx context.Context, host, repository, chainID string) *LayerEntry {
	var entry LayerEntry
	row := m.db.QueryRowContext(ctx, "select host, repo, chain_id, data_digest, data_size from overlaybd_layers where host=? and repo=? and chain_id=?", host, repository, chainID)
	if err := row.Scan(&entry.Host, &entry.Repository, &entry.ChainID, &entry.ConvertedDigest, &entry.DataSize); err != nil {
		return nil
	}
	return &entry
}

func (m *sqldb) GetCrossRepoLayerEntries(ctx context.Context, host, chainID string) []*LayerEntry {
	rows, err := m.db.QueryContext(ctx, "select host, repo, chain_id, data_digest, data_size from overlaybd_layers where host=? and chain_id=?", host, chainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		log.G(ctx).Infof("query error %v", err)
		return nil
	}
	var entries []*LayerEntry
	for rows.Next() {
		var entry LayerEntry
		err = rows.Scan(&entry.Host, &entry.Repository, &entry.ChainID, &entry.ConvertedDigest, &entry.DataSize)
		if err != nil {
			continue
		}
		entries = append(entries, &entry)
	}

	return entries
}

func (m *sqldb) DeleteLayerEntry(ctx context.Context, host, repository string, chainID string) error {
	_, err := m.db.Exec("delete from overlaybd_layers where host=? and repo=? and chain_id=?", host, repository, chainID)
	if err != nil {
		return fmt.Errorf("failed to remove invalid record in db: %w", err)
	}
	return nil
}

func (m *sqldb) CreateManifestEntry(ctx context.Context, host, repository, mediaType string, original, convertedDigest digest.Digest, size int64) error {
	_, err := m.db.ExecContext(ctx, "insert into overlaybd_manifests(host, repo, src_digest, out_digest, data_size, mediatype) values(?, ?, ?, ?, ?, ?)", host, repository, original, convertedDigest, size, mediaType)
	return err
}

func (m *sqldb) GetManifestEntryForRepo(ctx context.Context, host, repository, mediaType string, original digest.Digest) *ManifestEntry {
	var entry ManifestEntry
	row := m.db.QueryRowContext(ctx, "select host, repo, src_digest, out_digest, data_size, mediatype from overlaybd_manifests where host=? and repo=? and src_digest=? and mediatype=?", host, repository, original, mediaType)
	if err := row.Scan(&entry.Host, &entry.Repository, &entry.OriginalDigest, &entry.ConvertedDigest, &entry.DataSize, &entry.MediaType); err != nil {
		return nil
	}
	return &entry
}

func (m *sqldb) GetCrossRepoManifestEntries(ctx context.Context, host, mediaType string, original digest.Digest) []*ManifestEntry {
	rows, err := m.db.QueryContext(ctx, "select host, repo, src_digest, out_digest, data_size, mediatype from overlaybd_manifests where host=? and src_digest=? and mediatype=?", host, original, mediaType)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		log.G(ctx).Infof("query error %v", err)
		return nil
	}
	var entries []*ManifestEntry
	for rows.Next() {
		var entry ManifestEntry
		err = rows.Scan(&entry.Host, &entry.Repository, &entry.OriginalDigest, &entry.ConvertedDigest, &entry.DataSize, &entry.MediaType)
		if err != nil {
			continue
		}
		entries = append(entries, &entry)
	}

	return entries
}

func (m *sqldb) DeleteManifestEntry(ctx context.Context, host, repository, mediaType string, original digest.Digest) error {
	_, err := m.db.Exec("delete from overlaybd_manifests where host=? and repo=? and src_digest=? and mediatype=?", host, repository, original, mediaType)
	if err != nil {
		return fmt.Errorf("failed to remove invalid record in db: %w", err)
	}
	return nil
}
