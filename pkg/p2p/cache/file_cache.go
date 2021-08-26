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
}

func (f *fileCacheItem) Size() int64 {
	return f.fileSize
}

func (f *fileCacheItem) Key() string {
	return f.key
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

func (f *fileCacheItem) Val() interface{} {
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

// newFileCacheItem constructor for fileCacheItem
func newFileCacheItem(key string, fileSize int64, read func() ([]byte, error)) (*fileCacheItem, error) {
	err := os.MkdirAll(path.Dir(key), 0755)
	if err != nil {
		return nil, err
	}
	log.Debugf("Create %s: %d", key, atomic.AddInt32(&fdCnt, 1))
	file, err := os.OpenFile(key, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil || file == nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() != fileSize {
		buffer, err := read()
		if err == nil && int64(len(buffer)) != fileSize {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			if err := os.Remove(file.Name()); err != nil {
				log.Warnf("File %s remove error!", file.Name())
			}
			return nil, err
		}
		l, err := writeAll(file, buffer)
		if err == nil && int64(l) != fileSize {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			if err := os.Remove(file.Name()); err != nil {
				log.Warnf("File %s remove error!", file.Name())
			}
			return nil, err
		}
	}
	item := &fileCacheItem{key: key, fileSize: fileSize, file: file}
	return item, err
}

// fileCacheLRU file cache used lru policy
type fileCacheLRU struct {
	cache lruCache
}

// newFileCacheLRU constructor for fileCacheLRU
func newFileCacheLRU(limit int64) *fileCacheLRU {
	return &fileCacheLRU{newLRUCache(limit)}
}

func (c *fileCacheLRU) Get(key string) (*fileCacheItem, bool) {
	ret, hit := c.cache.Get(key)
	r, _ := ret.(*fileCacheItem)
	return r, hit
}

func (c *fileCacheLRU) Set(key string, val *fileCacheItem) {
	c.cache.Set(key, val)
}

func (c *fileCacheLRU) GetOrSet(key string, creator func(key string) (*fileCacheItem, error)) (*fileCacheItem, error) {
	ret, err := c.cache.GetOrSet(key, func(key string) (cacheItem, error) { return creator(key) })
	if err != nil {
		return nil, err
	}
	return ret.(*fileCacheItem), err
}

func (c *fileCacheLRU) Touch(key string) {
	c.cache.Touch(key)
}

func (c *fileCacheLRU) Sum() int64 {
	return c.cache.Sum()
}

func (c *fileCacheLRU) Limit() int64 {
	return c.cache.Limit()
}

func (c *fileCacheLRU) Expire() {
	c.cache.Expire()
}

func (c *fileCacheLRU) Del(key string) {
	c.cache.Del(key)
}
