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

package p2p

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

const cacheBlockSize int = 1 * 1024 * 1024

// RemoteFetcher is interface for fetching source data via remote access
type RemoteFetcher interface {
	// PreadRemote PRead like method to fetch file data starts from offset, length as `len(buf)`
	PreadRemote(buf []byte, offset int64) (int, error)
	// FstatRemote get file length by remote
	FstatRemote() (int64, error)
}

type remoteSource struct {
	req    *http.Request
	hp     HostPicker
	apikey string
}

func newRemoteSource(req *http.Request, hp HostPicker, APIKey string) RemoteFetcher {
	return &remoteSource{req, hp, APIKey}
}

func (f *remoteSource) PreadRemote(buff []byte, offset int64) (int, error) {
	fn := f.req.URL.String()
	upperHost := f.hp.GetHost(fn)
	url := fn
	if upperHost != "" {
		url = fmt.Sprintf("%s/%s/%s", upperHost, f.apikey, fn)
	}
	newReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return -1, err
	}
	for k, vv := range f.req.Header {
		vv2 := make([]string, len(vv))
		copy(vv2, vv)
		newReq.Header[k] = vv2
	}
	newReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, int64(len(buff))+offset-1))
	log.Infof("Fetching remote %s %s ", fn, newReq.Header.Get("Range"))
	resp, err := http.DefaultClient.Do(newReq)
	if err != nil || (resp.StatusCode != 200 && resp.StatusCode != 206) {
		log.Error(resp, err)
		return 0, FetchFailure{resp, err}
	}
	source := resp.Header.Get("X-P2P-Source")
	if source != "" {
		f.hp.PutHost(fn, source)
	} else {
		source = "origin"
	}
	log.Infof("Got remote %s %s from %s ", fn, newReq.Header.Get("Range"), source)
	return io.ReadFull(resp.Body, buff)
}

// FetchFailure error type for remote access failure, stores http response
type FetchFailure struct {
	resp *http.Response
	err  error
}

func (f FetchFailure) Error() string {
	return f.err.Error()
}

func (f *remoteSource) FstatRemote() (int64, error) {
	url := f.req.URL.String()
	newReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, FetchFailure{nil, err}
	}
	for k, vs := range f.req.Header {
		for _, v := range vs {
			newReq.Header.Add(k, v)
		}
	}
	newReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", 0, 0))
	newReq.Header.Del("X-P2P-Agent")
	resp, err := http.DefaultClient.Do(newReq)
	if err != nil || (resp.StatusCode != 200 && resp.StatusCode != 206) {
		return 0, FetchFailure{resp, err}
	}
	if resp.StatusCode == 200 {
		return resp.ContentLength, err
	}
	l := resp.ContentLength
	rs := resp.Header.Get("Content-Range")
	if rs == "" {
		return l, err
	}
	pos := strings.LastIndexByte(rs, '/')
	if pos < 0 {
		return l, err
	}
	l, _ = strconv.ParseInt(rs[pos+1:], 10, 64)
	return l, err
}

type prefetchTask struct {
	fn     string
	rf     RemoteFetcher
	offset int64
	count  int
}

// PFile provides file access methods for remote file with cache,
// able to work with `http.ServeContent`
type PFile struct {
	path   string
	fs     FS
	cur    int64
	size   int64
	Source RemoteFetcher
}

// Read data via file access
func (r *PFile) Read(p []byte) (n int, err error) {
	ret, err := r.ReadAt(p, r.cur)
	if err == nil {
		r.cur += int64(ret)
	}
	return ret, err
}

// Seek set offset of file
func (r *PFile) Seek(offset int64, whence int) (int64, error) {
	var err error = nil
	switch whence {
	case io.SeekCurrent:
		r.cur += offset
	case io.SeekStart:
		r.cur = offset
	case io.SeekEnd:
		r.cur = r.size
	}
	return r.cur, err
}

// ReadAt like pread
func (r *PFile) ReadAt(buff []byte, offset int64) (int, error) {
	fileSize, err := r.Fstat()
	if err != nil {
		return 0, err
	}
	left := alignDown(offset, int64(cacheBlockSize))
	supposeLen := int(min64(int64(cacheBlockSize), fileSize-left))
	retry := false
again:
	data, err := r.fs.cache.GetOrRefill(r.path, left, supposeLen, func() ([]byte, error) {
		data := make([]byte, supposeLen)
		ret, err := r.Source.PreadRemote(data, left)
		if err != nil && err != io.EOF {
			return nil, err
		} else if ret != supposeLen {
			return data, io.ErrUnexpectedEOF
		}
		return data, nil
	})
	if err != nil {
		r.fs.hp.ResetHost(r.path)
		if !retry {
			log.Errorf("Failed to read %s at %d, %s retry", r.path, offset, err.Error())
			retry = true
			goto again
		}
		log.Errorf("Failed to read %s at %d, %s", r.path, offset, err.Error())
		return 0, err
	}
	pos := int(offset - left)
	ret := min(len(buff), len(data)-pos)
	ret = copy(buff[:ret], data[pos:pos+ret])
	if offset+int64(len(buff)) > fileSize {
		err = io.EOF
	}
	return ret, err
}

// Prefetch launch prefetcher
func (r *PFile) Prefetch(offset int64, count int64) {
	go func() {
		fileSize, err := r.Fstat()
		if err != nil {
			return
		}
		for seg := range NewRangeSplit(offset, cacheBlockSize, count, fileSize).AllParts() {
			r.fs.preTask <- prefetchTask{
				fn:     r.path,
				rf:     r.Source,
				offset: seg.Index,
				count:  seg.Count,
			}
		}
	}()
}

// Fstat get file length
func (r *PFile) Fstat() (int64, error) {
	var hit bool
	r.size, hit = r.fs.cache.GetLen(r.path)
	if !hit {
		var err error
		r.size, err = r.Source.FstatRemote()
		if err != nil {
			return 0, err
		}
		r.fs.cache.PutLen(r.path, r.size)
	}
	return r.size, nil
}

// FS cached remote file system (RO)
type FS struct {
	cache        FileCachePool
	hp           HostPicker
	rwl          *sync.RWMutex
	apikey       string
	prefetchable bool
	preTask      chan prefetchTask
}

// Open a p2pFile object
func (fs FS) Open(path string, req *http.Request) (*PFile, error) {
	file := PFile{path, fs, 0, 0, newRemoteSource(req, fs.hp, fs.apikey)}
	fileSize, err := file.Source.FstatRemote()
	fs.cache.PutLen(path, fileSize)
	file.size = fileSize
	if fs.prefetchable {
		file.Prefetch(0, fileSize)
	}
	return &file, err
}

// Stat get file length
func (fs FS) Stat(path string, req *http.Request) (int64, error) {
	file, err := fs.Open(path, req)
	if err != nil {
		return 0, err
	}
	return file.Fstat()
}

func (fs FS) prefetcher() {
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
				log.Warnf("GetOrRefill %s fail!", seg.fn)
			}
		}
	}()
}

// FSConfig configuration for p2p.fs
type FSConfig struct {
	CachePool       FileCachePool
	HostPicker      HostPicker
	APIKey          string
	PrefetchWorkers int
}

// NewP2PFS constructor for p2p.fs
func NewP2PFS(cfg *FSConfig) *FS {
	fs := &FS{
		cache:        cfg.CachePool,
		hp:           cfg.HostPicker,
		rwl:          &sync.RWMutex{},
		apikey:       cfg.APIKey,
		preTask:      make(chan prefetchTask, cfg.PrefetchWorkers),
		prefetchable: cfg.PrefetchWorkers > 0,
	}
	for i := 0; i < cfg.PrefetchWorkers; i++ {
		fs.prefetcher()
	}
	return fs
}
