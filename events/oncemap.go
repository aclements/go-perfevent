// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package events

import "sync"

type onceMap[K comparable, V any] struct {
	m   sync.Map /*[K, onceMapEntry[V]]*/
	new func(K) (V, error)
}

type onceMapEntry[V any] struct {
	once sync.Once
	val  V
	err  error
}

func newOnceMap[K comparable, V any](new func(K) (V, error)) *onceMap[K, V] {
	return &onceMap[K, V]{new: new}
}

func (m *onceMap[K, V]) get(key K) (V, error) {
	var ent *onceMapEntry[V]
	entX, ok := m.m.Load(key)
	if ok {
		ent = entX.(*onceMapEntry[V])
	} else {
		// Try to store a new entry.
		ent = new(onceMapEntry[V])
		entX, ok = m.m.LoadOrStore(key, ent)
		if ok {
			// We lost a race.
			ent = entX.(*onceMapEntry[V])
		}
	}

	ent.once.Do(func() {
		ent.val, ent.err = m.new(key)
	})

	return ent.val, ent.err
}
