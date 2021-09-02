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

package fs

import (
	"io"
	"net/http"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/cache"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/hostselector"

	log "github.com/sirupsen/logrus"
)

// P2PFS cached remote file system (RO)
type P2PFS struct {
	cache        cache.FileCachePool
	hp           hostselector.HostPicker
	apikey       string
	prefetchable bool
	preTask      chan prefetchTask
}

// Open a p2pFile object
func (fs P2PFS) Open(path string, req *http.Request) (*P2PFile, error) {
	file := P2PFile{path, fs, 0, 0, newRemoteSource(req, fs.hp, fs.apikey)}
	fileSize, err := file.Fstat()
	if fs.prefetchable {
		file.Prefetch(0, fileSize)
	}
	return &file, err
}

// Stat get file length
func (fs P2PFS) Stat(path string, req *http.Request) (int64, error) {
	file, err := fs.Open(path, req)
	if err != nil {
		return 0, err
	}
	return file.Fstat()
}

// prefetcher will prefetch resource to accelerate read
func (fs P2PFS) prefetcher() {
	go func() {
		for seg := range fs.preTask {
			if _, err := fs.cache.GetOrRefill(seg.fn, seg.offset, seg.count, func() ([]byte, error) {
				data := make([]byte, seg.count)
				ret, err := seg.rf.PreadRemote(data, seg.offset)
				if err != nil && err != io.EOF {
					return nil, err
				} else if ret != seg.count {
					return data, io.ErrUnexpectedEOF
				}
				return data, nil
			}); err != nil {
				log.Warnf("GetOrRefill %s fail! %s", seg.fn, err)
			}
		}
	}()
}

// Config configuration for P2PFS
type Config struct {
	CachePool       cache.FileCachePool
	HostPicker      hostselector.HostPicker
	APIKey          string
	PrefetchWorkers int
}

// NewP2PFS constructor for P2PFS
func NewP2PFS(cfg *Config) *P2PFS {
	fs := &P2PFS{
		cache:        cfg.CachePool,
		hp:           cfg.HostPicker,
		apikey:       cfg.APIKey,
		preTask:      make(chan prefetchTask, cfg.PrefetchWorkers),
		prefetchable: cfg.PrefetchWorkers > 0,
	}
	for i := 0; i < cfg.PrefetchWorkers; i++ {
		fs.prefetcher()
	}
	return fs
}
