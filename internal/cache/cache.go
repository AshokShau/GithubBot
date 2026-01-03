package cache

import (
	"sync"
	"time"
)

type item[V any] struct {
	value      V
	expiration time.Time
}

type Cache[K comparable, V any] struct {
	items sync.Map
}

// New creates a new Cache instance
func New[K comparable, V any]() *Cache[K, V] {
	return &Cache[K, V]{}
}

// Set adds an item to the cache with a specific TTL
func (c *Cache[K, V]) Set(key K, value V, ttl time.Duration) {
	c.items.Store(key, item[V]{
		value:      value,
		expiration: time.Now().Add(ttl),
	})
}

// Get retrieves an item from the cache. Returns false if not found or expired.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	val, ok := c.items.Load(key)
	if !ok {
		var zero V
		return zero, false
	}

	itm := val.(item[V])
	if time.Now().After(itm.expiration) {
		c.items.Delete(key)
		var zero V
		return zero, false
	}

	return itm.value, true
}

// Delete removes an item from the cache
func (c *Cache[K, V]) Delete(key K) {
	c.items.Delete(key)
}

// Cleanup removes expired items
func (c *Cache[K, V]) Cleanup() {
	c.items.Range(func(key, value any) bool {
		itm := value.(item[V])
		if time.Now().After(itm.expiration) {
			c.items.Delete(key)
		}
		return true
	})
}
