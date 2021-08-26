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
	"os"
	"path/filepath"
	"strconv"
	"sync"

	log "github.com/sirupsen/logrus"
)

// Config set cache size, entry num and cache media(fs)
type Config struct {
	CacheSize  int64
	MaxEntry   int64
	CacheMedia string
}

// FileCachePool provides basic interface for cache access
type FileCachePool interface {
	// GetLen fetch value length if hit
	GetLen(path string) (int64, bool)
	// PutLen set value length
	PutLen(path string, length int64) bool
	// GetOrRefill try to fetch cache value, call `fetch` if not hit
	GetOrRefill(path string, offset int64, count int, fetch func() ([]byte, error)) ([]byte, error)
	// GetHost get P2P Host for key
	GetHost(path string) (string, bool)
	// PutHost store P2P Host for key
	PutHost(path string, host string) bool
	// DelHost clear P2P Host for key
	DelHost(path string)
}

type fileCachePoolImpl struct {
	cache *fileCacheLRU
	entry *memCacheLRU
	media string
	lock  sync.Mutex
}

func (c *fileCachePoolImpl) GetOrRefill(path string, offset int64, count int, fetch func() ([]byte, error)) ([]byte, error) {
	key := filepath.Join(c.media, path, strconv.FormatInt(offset, 10))
	var item *fileCacheItem
	{
		c.lock.Lock()
		defer c.lock.Unlock()
		var ok bool
		item, ok = c.cache.Get(key)
		if !ok {
			item = &fileCacheItem{}
			c.cache.Set(key, item)
		}
	}
	var value []byte
	{
		item.lock.Lock()
		defer item.lock.Unlock()
		if item.file != nil {
			value = item.Val().([]byte)
		}
		if len(value) == 0 {
			var err error
			item, err = newFileCacheItem(key, int64(count), fetch)
			if err != nil {
				return nil, err
			}
			value = item.Val().([]byte)
			c.cache.Set(key, item)
		}
	}
	return value, nil
}

func (c *fileCachePoolImpl) GetLen(path string) (int64, bool) {
	key := filepath.Join(path, "metainfo")
	val, found := c.entry.Get(key)
	if !found {
		return 0, false
	}
	return val.Val().(int64), true
}

func (c *fileCachePoolImpl) PutLen(path string, len int64) bool {
	key := filepath.Join(path, "metainfo")
	c.entry.Set(key, newMemCacheItem(key, len))
	return true
}

func (c *fileCachePoolImpl) GetHost(path string) (string, bool) {
	key := filepath.Join(path, "upstream")
	val, found := c.entry.Get(key)
	if !found {
		return "", false
	}
	return val.Val().(string), found
}

func (c *fileCachePoolImpl) PutHost(path string, host string) bool {
	key := filepath.Join(path, "upstream")
	c.entry.Set(key, newMemCacheItem(path, host))
	return true
}

func (c *fileCachePoolImpl) DelHost(path string) {
	key := filepath.Join(path, "upstream")
	c.entry.Del(key)
}

// NewCachePool creator for FileCachePool
func NewCachePool(config *Config) FileCachePool {
	if err := os.MkdirAll(config.CacheMedia, 0755); err != nil {
		log.Fatalf("Mkdir %s fail! %s", config.CacheMedia, err)
	}
	return &fileCachePoolImpl{
		cache: newFileCacheLRU(config.CacheSize),
		entry: newMemCacheLRU(config.MaxEntry),
		media: config.CacheMedia,
	}
}
