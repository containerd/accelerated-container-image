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

	"github.com/containerd/containerd/log"
	"github.com/pkg/errors"

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

func (m *sqldb) GetEntryForRepo(ctx context.Context, host string, repository string, chainID string) *Entry {
	var entry Entry

	row := m.db.QueryRowContext(ctx, "select host, repo, chain_id, data_digest, data_size from overlaybd_layers where host=? and repo=? and chain_id=?", host, repository, chainID)
	if err := row.Scan(&entry.Host, &entry.Repository, &entry.ChainID, &entry.ConvertedDigest, &entry.DataSize); err != nil {
		return nil
	}

	return &entry
}

func (m *sqldb) GetCrossRepoEntries(ctx context.Context, host string, chainID string) []*Entry {
	rows, err := m.db.QueryContext(ctx, "select host, repo, chain_id, data_digest, data_size from overlaybd_layers where host=? and chain_id=?", host, chainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		log.G(ctx).Infof("query error %v", err)
		return nil
	}
	var entries []*Entry
	for rows.Next() {
		var entry Entry
		err = rows.Scan(&entry.Host, &entry.Repository, &entry.ChainID, &entry.ConvertedDigest, &entry.DataSize)
		if err != nil {
			continue
		}
		entries = append(entries, &entry)
	}

	return entries
}

func (m *sqldb) CreateEntry(ctx context.Context, host string, repository string, convertedDigest digest.Digest, chainID string, size int64) error {
	_, err := m.db.ExecContext(ctx, "insert into overlaybd_layers(host, repo, chain_id, data_digest, data_size) values(?, ?, ?, ?, ?)", host, repository, chainID, convertedDigest, size)
	return err
}

func (m *sqldb) DeleteEntry(ctx context.Context, host string, repository string, chainID string) error {
	_, err := m.db.Exec("delete from overlaybd_layers where host=? and repo=? and chain_id=?", host, repository, chainID)
	if err != nil {
		return errors.Wrapf(err, "failed to remove invalid record in db")
	}
	return nil
}
