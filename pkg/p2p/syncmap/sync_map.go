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

package syncmap

import (
	"sync"
)

// SyncMap is similar as sync.Map and add GetOrSet by creator
type SyncMap interface {
	// Get item from SyncMap
	Get(key string) (interface{}, bool)
	// Set item to SyncMap
	Set(key string, val interface{})
	// GetOrSet get item first, if not found, it will use creator to create one and return
	GetOrSet(key string, creator func(key string) (interface{}, error)) (interface{}, error)
	// Remove item from SyncMap
	Remove(key string)
}

// rwSyncMapItem is RwSyncMap item
type rwSyncMapItem struct {
	val interface{}
	mtx sync.RWMutex
}

// RwSyncMap is a SyncMap implementation
type RwSyncMap struct {
	kv   map[string]*rwSyncMapItem
	lock sync.RWMutex
}

// NewSyncMap constructor for SyncMap
func NewSyncMap() SyncMap {
	return &RwSyncMap{kv: make(map[string]*rwSyncMapItem)}
}

func (m *RwSyncMap) Get(key string) (interface{}, bool) {
	m.lock.RLock()
	val, hit := m.kv[key]
	m.lock.RUnlock()
	if !hit {
		return nil, false
	}
	val.mtx.RLock()
	defer val.mtx.RUnlock()
	if val.val == nil {
		return nil, false
	}
	return val.val, true
}

func (m *RwSyncMap) Set(key string, val interface{}) {
	if val == nil {
		return
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	m.kv[key] = &rwSyncMapItem{val: val}
}

func (m *RwSyncMap) Remove(key string) {
	m.lock.Lock()
	defer m.lock.Unlock()
	delete(m.kv, key)
}

func (m *RwSyncMap) GetOrSet(key string, creator func(key string) (interface{}, error)) (interface{}, error) {
	ret, hit := m.Get(key)
	if hit {
		return ret, nil
	}
	m.lock.Lock()
	item, hit := m.kv[key]
	if !hit {
		item = &rwSyncMapItem{val: nil}
		m.kv[key] = item
	}
	m.lock.Unlock()
	item.mtx.Lock()
	defer item.mtx.Unlock()
	var err error
	if item.val == nil {
		ret, err = creator(key)
		if err != nil {
			return nil, err
		}
		item.val = ret
	}
	return item.val, err
}
