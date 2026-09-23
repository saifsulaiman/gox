package goweb

import (
	"sync"
)

// call is an in-flight or completed singleflight request.
type sfCall struct {
	wg  sync.WaitGroup
	val any
	err error
}

// SingleflightGroup suppresses duplicate function executions with the same key.
type SingleflightGroup struct {
	mu sync.Mutex
	m  map[string]*sfCall
}

// NewSingleflightGroup initializes a singleflight deduplication engine.
func NewSingleflightGroup() *SingleflightGroup {
	return &SingleflightGroup{
		m: make(map[string]*sfCall),
	}
}

// Do executes and returns the results of the given function, making
// sure that only one execution is in-flight for a given key at a
// time. If a duplicate comes in, the duplicate caller waits for the
// original to complete and receives the identical result.
func (g *SingleflightGroup) Do(key string, fn func() (any, error)) (any, error) {
	g.mu.Lock()
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := new(sfCall)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}

var globalSingleflight = NewSingleflightGroup()

// Singleflight collapses concurrent calls with the same key into a single execution.
func (c *Context) Singleflight(key string, fn func() (any, error)) (any, error) {
	return globalSingleflight.Do(key, fn)
}
