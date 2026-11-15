// Package cache — маленький потокобезопасный TTL-кэш в памяти.
// Используется, чтобы не дёргать CoinGecko на каждый чих:
// у бесплатного тарифа жёсткие лимиты запросов.
package cache

import (
	"sync"
	"time"
)

// Cache хранит значения типа T с индивидуальным временем жизни (TTL).
// Нулевое значение не готово к работе — создавайте через New.
type Cache[T any] struct {
	mu    sync.Mutex
	items map[string]item[T]
}

type item[T any] struct {
	val T
	exp time.Time
}

// New создаёт пустой кэш и запускает фоновую уборку просроченных записей.
func New[T any](cleanupEvery time.Duration) *Cache[T] {
	c := &Cache[T]{items: make(map[string]item[T])}
	go c.janitor(cleanupEvery)
	return c
}

// Get возвращает значение по ключу. Просроченные записи считаются
// отсутствующими и удаляются прямо здесь — «ленивая» чистка.
func (c *Cache[T]) Get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	it, ok := c.items[key]
	if !ok {
		var zero T
		return zero, false
	}
	if time.Now().After(it.exp) {
		delete(c.items, key)
		var zero T
		return zero, false
	}
	return it.val, true
}

// Set кладёт значение с заданным TTL.
func (c *Cache[T]) Set(key string, val T, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = item[T]{val: val, exp: time.Now().Add(ttl)}
}

// janitor — фоновая горутина: раз в cleanupEvery удаляет всё просроченное,
// чтобы память не росла, если к каким-то ключам больше не обращаются.
func (c *Cache[T]) janitor(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		c.mu.Lock()
		for k, it := range c.items {
			if now.After(it.exp) {
				delete(c.items, k)
			}
		}
		c.mu.Unlock()
	}
}
