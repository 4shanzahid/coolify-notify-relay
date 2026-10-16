// Package ttl provides a small in-memory map whose entries expire after a fixed duration.
package ttl

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value   V
	expires time.Time
}

// Cache is a concurrency-safe map with per-entry expiry. Expired entries are
// swept lazily on writes, so memory stays bounded by the write rate × TTL.
type Cache[K comparable, V any] struct {
	mu     sync.Mutex
	ttl    time.Duration
	items  map[K]entry[V]
	writes int
	now    func() time.Time
}

func New[K comparable, V any](ttl time.Duration) *Cache[K, V] {
	return &Cache[K, V]{ttl: ttl, items: make(map[K]entry[V]), now: time.Now}
}

func (c *Cache[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[k]
	if !ok || c.now().After(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

func (c *Cache[K, V]) Set(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(k, v)
}

// Add stores v only if k is absent (or expired) and reports whether it did.
func (c *Cache[K, V]) Add(k K, v V) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok && !c.now().After(e.expires) {
		return false
	}
	c.setLocked(k, v)
	return true
}

func (c *Cache[K, V]) Delete(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, k)
}

func (c *Cache[K, V]) setLocked(k K, v V) {
	now := c.now()
	c.items[k] = entry[V]{value: v, expires: now.Add(c.ttl)}
	c.writes++
	if c.writes%128 == 0 {
		for key, e := range c.items {
			if now.After(e.expires) {
				delete(c.items, key)
			}
		}
	}
}
