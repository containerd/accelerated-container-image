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

package synclist

import (
	"container/list"
	"sync"
)

// SyncList is thread(routine) safe list
type SyncList interface {
	// PushFront push value to list front and return list.Element
	PushFront(v interface{}) *list.Element
	// Remove the list.Element
	Remove(e *list.Element) interface{}
	// MoveToFront move the list.Element to list front
	MoveToFront(e *list.Element)
	// Front get the list front
	Front() *list.Element
}

// RwSyncList is a SyncList implementation
type RwSyncList struct {
	l    *list.List
	lock sync.RWMutex
}

// NewSyncList constructor for SyncList
func NewSyncList() SyncList {
	return &RwSyncList{l: list.New()}
}

func (m *RwSyncList) PushFront(item interface{}) *list.Element {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.l.PushFront(item)
}

func (m *RwSyncList) Remove(e *list.Element) interface{} {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.l.Remove(e)
}

func (m *RwSyncList) MoveToFront(e *list.Element) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.l.MoveToFront(e)
}

func (m *RwSyncList) Front() *list.Element {
	m.lock.RLock()
	defer m.lock.RUnlock()
	return m.l.Front()
}
