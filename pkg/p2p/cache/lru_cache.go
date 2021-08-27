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
	"container/list"
	"sync/atomic"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/synclist"
	"github.com/alibaba/accelerated-container-image/pkg/p2p/syncmap"

	log "github.com/sirupsen/logrus"
)

// fdCnt count open fd num
var fdCnt int32

// cacheItem is interface store value
type cacheItem interface {
	// Key get cacheItem key
	Key() string
	// Val get cacheItem value
	Val() interface{}
	// Size get cacheItem Size
	Size() int64
	// Drop cacheItem
	Drop()
}

// lruCache is interface to cache use lru policy
type lruCache interface {
	// Get cacheItem
	Get(key string) (cacheItem, bool)
	// Set cacheItem
	Set(key string, val cacheItem)
	// GetOrSet get cacheItem, if not found, create it and return
	GetOrSet(key string, creator func(key string) (cacheItem, error)) (cacheItem, error)
	// Del cacheItem
	Del(key string)
	// Touch cacheItem, it will reset lru
	Touch(key string)
	// Sum return cache sum
	Sum() int64
	// Limit return cache limit
	Limit() int64
	// Expire cacheItem
	Expire()
}

// lruSyncMapCache is a lruCache implementation
type lruSyncMapCache struct {
	kv       syncmap.SyncMap // map of `*list.Element` stores `CacheItem`
	l        synclist.SyncList
	s, limit int64
}

// newLRUCache constructor for lruCache
func newLRUCache(limit int64) lruCache {
	atomic.StoreInt32(&fdCnt, 0)
	return &lruSyncMapCache{
		kv:    syncmap.NewSyncMap(),
		l:     synclist.NewSyncList(),
		limit: limit,
	}
}

func (m *lruSyncMapCache) Del(key string) {
	elem, hit := m.kv.Get(key)
	if hit {
		v := m.l.Remove(elem.(*list.Element))
		item := v.(cacheItem)
		m.s -= item.Size()
		m.kv.Remove(key)
		item.Drop()
	}
}

func (m *lruSyncMapCache) Get(key string) (cacheItem, bool) {
	defer m.Expire()
	elem, hit := m.kv.Get(key)
	if hit {
		m.l.MoveToFront(elem.(*list.Element))
		return elem.(*list.Element).Value.(cacheItem), true
	}
	return nil, false
}

func (m *lruSyncMapCache) Set(key string, val cacheItem) {
	defer m.Expire()
	if val == nil {
		return
	}
	elem, err := m.kv.GetOrSet(key, func(_ string) (interface{}, error) {
		m.s += val.Size()
		return m.l.PushFront(val), nil
	})
	if err != nil {
		return
	}
	e := elem.(*list.Element)
	if e.Value != nil {
		m.s -= e.Value.(cacheItem).Size()
	}
	e.Value = val
	m.s += val.Size()
	m.l.MoveToFront(e)
}

func (m *lruSyncMapCache) GetOrSet(key string, creator func(key string) (cacheItem, error)) (cacheItem, error) {
	defer m.Expire()
	elem, err := m.kv.GetOrSet(key, func(_ string) (interface{}, error) {
		val, err := creator(key)
		if err != nil {
			return nil, err
		}
		m.s += val.Size()
		ret := m.l.PushFront(val)
		return ret, err
	})
	if err != nil {
		return nil, err
	}
	if elem == nil {
		log.Fatal("ERROR, err", err)
	}
	e := elem.(*list.Element)
	m.l.MoveToFront(e)
	return e.Value.(cacheItem), err
}

func (m *lruSyncMapCache) Touch(key string) {
	m.Get(key)
}

func (m *lruSyncMapCache) Sum() int64 {
	return m.s
}

func (m *lruSyncMapCache) Limit() int64 {
	return m.limit
}

func (m *lruSyncMapCache) Expire() {
	for m.s > m.limit {
		front := m.l.Remove(m.l.Front())
		if front == nil {
			return
		}
		item := front.(cacheItem)
		m.s -= item.Size()
		m.kv.Remove(item.Key())
		item.Drop()
	}
}
