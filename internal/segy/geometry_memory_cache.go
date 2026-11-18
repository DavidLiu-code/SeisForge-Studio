package segy

import (
	"container/list"
	"sync"
)

// geometryIndexMemoryCache retains completed immutable indexes, never Readers.
// Its entry count bounds per-key overhead; the byte budget bounds slice backing
// storage and reserves any dense lookup that prepare may lazily allocate later.
// It does not affect on-disk .lidx files or the lifetime of callers' pointers.
type geometryIndexMemoryCache struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	recency    list.List // most recently reused at the front
	bytes      uint64
	maxEntries int
	maxBytes   uint64
}

type geometryMemoryEntry struct {
	key   string
	index *GeometryIndex
	bytes uint64
}

func newGeometryMemoryCache(maxEntries int, maxBytes uint64) *geometryIndexMemoryCache {
	return &geometryIndexMemoryCache{entries: make(map[string]*list.Element), maxEntries: maxEntries, maxBytes: maxBytes}
}

func geometryMemoryWeight(key string, idx *GeometryIndex) uint64 {
	// This deliberately overcounts shared backing arrays and metadata. Use
	// capacity, not length: an index can retain a larger backing allocation.
	bytes := uint64(256)
	add := func(count int, width uint64) {
		if uint64(count) > (^uint64(0)-bytes)/width {
			bytes = ^uint64(0)
			return
		}
		bytes += uint64(count) * width
	}
	add(len(key), 1)
	add(len(idx.SourcePath), 1)
	add(cap(idx.InlineValues), 4)
	add(cap(idx.CrosslineValues), 4)
	add(cap(idx.TraceNumbers), 8)
	add(cap(idx.RowOfTrace), 4)
	add(cap(idx.ColOfTrace), 4)
	// Do not call prepare or read its mutable denseGrid here. Reserve the same
	// maximum that prepareContext permits, without changing lazy preparation.
	rows, cols := len(idx.InlineValues), len(idx.CrosslineValues)
	if rows > 0 && cols > 0 && rows <= 20_000_000/cols {
		add(rows*cols, 4)
	}
	return bytes
}

func (c *geometryIndexMemoryCache) remove(element *list.Element) {
	entry := element.Value.(*geometryMemoryEntry)
	delete(c.entries, entry.key)
	c.recency.Remove(element)
	c.bytes -= entry.bytes
}

func (c *geometryIndexMemoryCache) Store(key string, idx *GeometryIndex) {
	if key == "" || idx == nil {
		return
	}
	weight := geometryMemoryWeight(key, idx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous := c.entries[key]; previous != nil {
		c.remove(previous)
	}
	// One oversized index remains usable by its caller and from disk, but
	// must not displace all reusable small indexes or exceed the RAM budget.
	if c.maxEntries <= 0 || weight > c.maxBytes {
		return
	}
	for len(c.entries) >= c.maxEntries || c.bytes > c.maxBytes-weight {
		c.remove(c.recency.Back())
	}
	entry := &geometryMemoryEntry{key: key, index: idx, bytes: weight}
	c.entries[key] = c.recency.PushFront(entry)
	c.bytes += weight
}

func (c *geometryIndexMemoryCache) Load(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element := c.entries[key]; element != nil {
		c.recency.MoveToFront(element)
		return element.Value.(*geometryMemoryEntry).index, true
	}
	return nil, false
}

func (c *geometryIndexMemoryCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element := c.entries[key]; element != nil {
		c.remove(element)
	}
}

// Range snapshots only bounded cache metadata and invokes callbacks unlocked,
// allowing diagnostics or cleanup to delete entries without deadlocking.
func (c *geometryIndexMemoryCache) Range(visit func(key, value any) bool) {
	c.mu.Lock()
	entries := make([]*geometryMemoryEntry, 0, len(c.entries))
	for _, element := range c.entries {
		entries = append(entries, element.Value.(*geometryMemoryEntry))
	}
	c.mu.Unlock()
	for _, entry := range entries {
		if !visit(entry.key, entry.index) {
			return
		}
	}
}
