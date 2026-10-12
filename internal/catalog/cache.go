// Package catalog caches each connection's schema.
package catalog

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/anderson-0/qh/internal/engine"
)

const buildTimeout = 60 * time.Second

// Builder introspects a database.
type Builder func(ctx context.Context) (*engine.Catalog, error)

type entry struct {
	version int64
	cat     *engine.Catalog
	built   time.Time
}

// Cache holds one catalog per connection id, rebuilt when its TTL passes,
// the connection's version changes, or it is invalidated.
type Cache struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]entry
	gens    map[string]uint64
	epoch   uint64 // bumped by InvalidateAll
	group   singleflight.Group
}

// New returns a cache whose entries live for ttl.
func New(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, now: time.Now, entries: map[string]entry{}, gens: map[string]uint64{}}
}

// Get returns the catalog for connection id at version. Concurrent callers
// share one build; a caller whose context ends stops waiting without
// cancelling the build for the others.
func (c *Cache) Get(ctx context.Context, id string, version int64, build Builder) (*engine.Catalog, error) {
	c.mu.Lock()
	if e, ok := c.entries[id]; ok && e.version == version && c.now().Sub(e.built) < c.ttl {
		c.mu.Unlock()
		return e.cat, nil
	}
	gen, epoch := c.gens[id], c.epoch
	c.mu.Unlock()

	key := fmt.Sprintf("%s@%d#%d.%d", id, version, gen, epoch)
	ch := c.group.DoChan(key, func() (any, error) {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
		defer cancel()
		cat, err := build(bctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.gens[id] == gen && c.epoch == epoch {
			c.entries[id] = entry{version: version, cat: cat, built: c.now()}
		}
		c.mu.Unlock()
		return cat, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*engine.Catalog), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Invalidate drops the cached catalog for id, including any build in progress.
func (c *Cache) Invalidate(id string) {
	c.mu.Lock()
	delete(c.entries, id)
	c.gens[id]++
	c.mu.Unlock()
}

// InvalidateAll drops every cached catalog, including builds in progress.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	c.epoch++
	c.entries = map[string]entry{}
	c.mu.Unlock()
}
