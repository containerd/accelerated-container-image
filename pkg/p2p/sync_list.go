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
	"container/list"
	"sync"
)

type syncList interface {
	PushFront(v interface{}) *list.Element
	Remove(e *list.Element) interface{}
	MoveToFront(e *list.Element)
	Front() *list.Element
}

type rwSyncList struct {
	syncList
	l    *list.List
	lock sync.RWMutex
}

func (m *rwSyncList) PushFront(item interface{}) *list.Element {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.l.PushFront(item)
}

func (m *rwSyncList) Remove(e *list.Element) interface{} {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.l.Remove(e)
}

func (m *rwSyncList) MoveToFront(e *list.Element) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.l.MoveToFront(e)
}

func (m *rwSyncList) Front() *list.Element {
	m.lock.RLock()
	defer m.lock.RUnlock()
	return m.l.Front()
}
