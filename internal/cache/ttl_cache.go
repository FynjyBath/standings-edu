package cache

import (
	"sync"
	"time"
)

type item[T any] struct {
	value     T
	expiresAt time.Time
}

type TTLCache[T any] struct {
	mu    sync.RWMutex
	data  map[string]item[T]
	ttl   time.Duration
	nowFn func() time.Time
}

func NewTTLCache[T any](ttl time.Duration) *TTLCache[T] {
	return &TTLCache[T]{
		data:  make(map[string]item[T]),
		ttl:   ttl,
		nowFn: time.Now,
	}
}

func (c *TTLCache[T]) Get(key string) (T, bool) {
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

func (c *TTLCache[T]) Set(key string, value T) {
	c.mu.Lock()
	c.data[key] = item[T]{
		value:     value,
		expiresAt: c.nowFn().Add(c.ttl),
	}
	c.mu.Unlock()
}
