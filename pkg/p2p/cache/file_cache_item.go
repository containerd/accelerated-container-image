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

package cache

import (
	"io"
	"os"
	"path"
	"sync"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
)

var fdCnt int32

// fileCacheItem is a cacheItem implementation
type fileCacheItem struct {
	key      string
	fileSize int64
	file     *os.File
	lock     sync.RWMutex
}

func (f *fileCacheItem) Drop() {
	f.lock.Lock()
	defer f.lock.Unlock()
	log.Debugf("Drop %s: %d", f.file.Name(), atomic.AddInt32(&fdCnt, -1))
	if err := f.file.Close(); err != nil {
		log.Warnf("File %s close failed!", f.file.Name())
	}
	if err := os.Remove(f.file.Name()); err != nil {
		log.Warnf("File %s remove failed!", f.file.Name())
	}
	f.file = nil
}

// readAllByHead read file content
func readAllByHead(file *os.File) ([]byte, error) {
	offset := int64(0)
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	ret := make([]byte, info.Size())
	for offset < info.Size() && err == nil {
		var l int
		l, err = file.ReadAt(ret[offset:], offset)
		offset += int64(l)
	}
	if err == io.EOF {
		err = nil
	}
	return ret[:offset], err
}

func (f *fileCacheItem) Val() []byte {
	ret, err := readAllByHead(f.file)
	if err != nil {
		return nil
	}
	return ret
}

// writeAll write file content
func writeAll(file *os.File, buff []byte) (int, error) {
	offset := 0
	err := file.Truncate(0)
	if err != nil {
		return 0, err
	}
	for err == nil && offset < len(buff) {
		l := 0
		l, err = file.Write(buff[offset:])
		offset += l
	}
	return offset, err
}

func newFileCacheItem(key string, fileSize int) (*fileCacheItem, error) {
	cacheItem := &fileCacheItem{key: key}
	if err := os.MkdirAll(path.Dir(key), 0755); err != nil {
		return nil, err
	}
	log.Debugf("Create %s: %d", key, atomic.AddInt32(&fdCnt, 1))
	var err error
	if cacheItem.file, err = os.OpenFile(key, os.O_CREATE|os.O_RDWR, 0644); err != nil {
		return nil, err
	}
	cacheItem.fileSize = int64(fileSize)
	return cacheItem, nil
}

func (f *fileCacheItem) Fill(read func() ([]byte, error)) error {
	buffer, err := read()
	if err == nil && int64(len(buffer)) != f.fileSize {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		if err := os.Remove(f.file.Name()); err != nil {
			log.Warnf("File %s remove fail! %s", f.file.Name(), err)
		}
		return err
	}
	l, err := writeAll(f.file, buffer)
	if err == nil && int64(l) != f.fileSize {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		if err := os.Remove(f.file.Name()); err != nil {
			log.Warnf("File %s remove fail! %s", f.file.Name(), err)
		}
		return err
	}
	return nil
}
