package standings

import (
	"sync"
	"time"
)

type cacheItem[T any] struct {
	value     T
	expiresAt time.Time
}

type ttlCache[T any] struct {
	mu    sync.RWMutex
	data  map[string]cacheItem[T]
	ttl   time.Duration
	nowFn func() time.Time
}

func newTTLCache[T any](ttl time.Duration) *ttlCache[T] {
	return &ttlCache[T]{
		data:  make(map[string]cacheItem[T]),
		ttl:   ttl,
		nowFn: time.Now,
	}
}

func (c *ttlCache[T]) Get(key string) (T, bool) {
	c.mu.RLock()
	it, ok := c.data[key]
	c.mu.RUnlock()
	if !ok {
		var zero T
		return zero, false
	}

	if c.nowFn().After(it.expiresAt) {
		c.mu.Lock()
		delete(c.data, key)
		c.mu.Unlock()
		var zero T
		return zero, false
	}

	return it.value, true
}

func (c *ttlCache[T]) Set(key string, value T) {
	c.mu.Lock()
	c.data[key] = cacheItem[T]{
		value:     value,
		expiresAt: c.nowFn().Add(c.ttl),
	}
	c.mu.Unlock()
}
