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

	"github.com/alibaba/accelerated-container-image/pkg/p2p/util"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/rangesplit"

	log "github.com/sirupsen/logrus"
)

const cacheBlockSize int = 1 * 1024 * 1024

type prefetchTask struct {
	fn     string
	rf     RemoteFetcher
	offset int64
	count  int
}

// P2PFile provides file access methods for remote file with cache,
// able to work with `http.ServeContent`
type P2PFile struct {
	path   string
	fs     P2PFS
	cur    int64
	size   int64
	Source RemoteFetcher
}

// Read data via file access
func (r *P2PFile) Read(p []byte) (n int, err error) {
	ret, err := r.ReadAt(p, r.cur)
	if err == nil {
		r.cur += int64(ret)
	}
	return ret, err
}

// Seek set offset of file
func (r *P2PFile) Seek(offset int64, whence int) (int64, error) {
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
func (r *P2PFile) ReadAt(buff []byte, offset int64) (int, error) {
	fileSize, err := r.Fstat()
	if err != nil {
		return 0, err
	}
	left := rangesplit.AlignDown(offset, int64(cacheBlockSize))
	supposeLen := int(util.Min64(int64(cacheBlockSize), fileSize-left))
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
	ret := util.Min(len(buff), len(data)-pos)
	ret = copy(buff[:ret], data[pos:pos+ret])
	if offset+int64(len(buff)) > fileSize {
		err = io.EOF
	}
	return ret, err
}

// Prefetch launch prefetcher
func (r *P2PFile) Prefetch(offset int64, count int64) {
	go func() {
		fileSize, err := r.Fstat()
		if err != nil {
			return
		}
		for seg := range rangesplit.NewRangeSplit(offset, cacheBlockSize, count, fileSize).AllParts() {
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
func (r *P2PFile) Fstat() (int64, error) {
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
