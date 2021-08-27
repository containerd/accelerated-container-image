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

// memCacheItem is a cacheItem implementation
type memCacheItem struct {
	key string
	val interface{}
}

func (f *memCacheItem) Drop() {
}

func (f *memCacheItem) Size() int64 {
	return 1
}

func (f *memCacheItem) Key() string {
	return f.key
}

func (f *memCacheItem) Val() interface{} {
	return f.val
}

// memCacheLRU memory cache used lru policy
type memCacheLRU struct {
	cache lruCache
}

// newMemCacheItem constructor for memCacheItem
func newMemCacheItem(key string, val interface{}) *memCacheItem {
	return &memCacheItem{key, val}
}

func newMemCacheLRU(limit int64) *memCacheLRU {
	return &memCacheLRU{newLRUCache(limit)}
}

func (c *memCacheLRU) Get(key string) (*memCacheItem, bool) {
	ret, hit := c.cache.Get(key)
	if ret == nil {
		return nil, hit
	}
	return ret.(*memCacheItem), true
}

func (c *memCacheLRU) Set(key string, val *memCacheItem) {
	c.cache.Set(key, val)
}

func (c *memCacheLRU) GetOrSet(key string, creator func(key string) (*memCacheItem, error)) (*memCacheItem, error) {
	ret, err := c.cache.GetOrSet(key, func(key string) (cacheItem, error) { return creator(key) })
	if ret == nil {
		return nil, err
	}
	return ret.(*memCacheItem), err
}

func (c *memCacheLRU) Touch(key string) {
	c.cache.Touch(key)
}

func (c *memCacheLRU) Sum() int64 {
	return c.cache.Sum()
}

func (c *memCacheLRU) Limit() int64 {
	return c.cache.Limit()
}

func (c *memCacheLRU) Expire() {
	c.cache.Expire()
}

func (c *memCacheLRU) Del(key string) {
	c.cache.Del(key)
}
