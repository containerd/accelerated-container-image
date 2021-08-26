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

package hostselector

import (
	"math/rand"
	"sync"
	"time"

	"github.com/alibaba/accelerated-container-image/pkg/p2p/cache"
)

// HostPicker is an interface
// to keep p2p host information
type HostPicker interface {
	// GetHost query url should fetch from which host
	// returns empty stream shows data should fetch from source
	// non-empty string present upstream p2p host
	GetHost(url string) string
	// PutHost stores query url matching source
	PutHost(url string, source string)
	// ResetHost clear up url matched source if exists
	ResetHost(url string)
}

// ChildrenManager is the
type ChildrenManager interface {
	// TryAccept try to accept a new host as child
	// return result
	TryAccept(url string, host string) (bool, string)
}

type randomHostPicker struct {
	roots []string
	pool  cache.FileCachePool
}

func (h *randomHostPicker) chooseRoot() string {
	if len(h.roots) == 0 {
		return ""
	}
	return h.roots[rand.Int()%len(h.roots)]
}

func (h *randomHostPicker) GetHost(url string) string {
	host, found := h.pool.GetHost(url)
	if !found {
		return h.chooseRoot()
	}
	return host
}

func (h *randomHostPicker) PutHost(url string, source string) {
	h.pool.PutHost(url, source)
}

func (h *randomHostPicker) ResetHost(url string) {
	h.pool.DelHost(url)
}

// NewHostPicker creator of random host picker
func NewHostPicker(roots []string, pool cache.FileCachePool) HostPicker {
	return &randomHostPicker{roots, pool}
}

type singleFileChildren struct {
	children   map[string]time.Time
	limit      int
	expiration time.Duration
	mtx        sync.RWMutex
}

func (c *singleFileChildren) TryAccept(host string) (bool, string) {
	c.expire()
	c.mtx.RLock()
	_, exist := c.children[host]
	c.mtx.RUnlock()
	if exist || len(c.children) < c.limit {
		c.mtx.Lock()
		c.children[host] = time.Now()
		c.mtx.Unlock()
		return true, ""
	}
	return false, c.Choose()
}

func (c *singleFileChildren) Choose() string {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	l := len(c.children)
	rd := rand.Intn(l)
	for k := range c.children {
		if rd == 0 {
			return k
		}
		rd--
	}
	return ""
}

func (c *singleFileChildren) expire() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for k, v := range c.children {
		if time.Since(v) > c.expiration {
			delete(c.children, k)
		}
	}
}

type fileChildren struct {
	children   map[string]*singleFileChildren
	limit      int
	expiration time.Duration
	mtx        sync.RWMutex
}

func (c *fileChildren) Get(key string) *singleFileChildren {
	c.mtx.RLock()
	ret := c.children[key]
	c.mtx.RUnlock()
	if ret == nil {
		c.mtx.Lock()
		ret = c.children[key]
		if ret == nil {
			ret = &singleFileChildren{
				children:   make(map[string]time.Time),
				limit:      c.limit,
				expiration: c.expiration}
			c.children[key] = ret
		}
		c.mtx.Unlock()
	}
	return ret
}

type limitedChildrenManager struct {
	children *fileChildren
}

func (m *limitedChildrenManager) TryAccept(url string, host string) (bool, string) {
	return m.children.Get(url).TryAccept(host)
}

// NewLimitedChildrenManager creator of limited children manager
func NewLimitedChildrenManager(limit int, expiration time.Duration) ChildrenManager {
	return &limitedChildrenManager{
		children: &fileChildren{
			children:   make(map[string]*singleFileChildren),
			limit:      limit,
			expiration: expiration,
		},
	}
}
