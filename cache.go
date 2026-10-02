package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	negativeTTL   = 30 * time.Second
	sourceTimeout = 5 * time.Second
)

type refresh struct {
	done  chan struct{}
	err   error               // published by closing done
	names map[string]struct{} // misses to publish before releasing waiters
}

// nodeCache owns refresh coordination and confirmed misses. Known names never
// wait for I/O or expire on a source failure. An unknown name is only declared
// absent after a successful refresh; errors are not cached as nonexistence.
type nodeCache struct {
	source nodeSource
	mu     sync.Mutex
	nodes  *nodeMap
	misses map[string]time.Time
	flight *refresh
}

func newCache(source nodeSource) *nodeCache {
	return &nodeCache{source: source, misses: make(map[string]time.Time)}
}

// lookup returns the current view, refreshing for an unconfirmed miss. Empty
// name requests zone metadata and only needs a refresh if no view exists yet.
func (c *nodeCache) lookup(ctx context.Context, name string) (*nodeMap, error) {
	c.mu.Lock()
	if c.nodes != nil {
		_, known := c.nodes.Hosts[name]
		if name == "" || known || time.Now().Before(c.misses[name]) {
			nodes := c.nodes
			c.mu.Unlock()
			return nodes, nil
		}
	}
	// Reserve the refresh while still holding the lookup lock, so concurrent
	// misses cannot queue redundant refreshes after the first one completes.
	f, leader := c.beginRefresh(name)
	c.mu.Unlock()
	err := c.finishRefresh(ctx, f, leader)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nodes != nil {
		if _, known := c.nodes.Hosts[name]; name == "" || known {
			return c.nodes, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return c.nodes, nil
}

// beginRefresh is called with mu held. Only the leader contacts tailscaled.
func (c *nodeCache) beginRefresh(name string) (*refresh, bool) {
	leader := c.flight == nil
	if leader {
		c.flight = &refresh{done: make(chan struct{}), names: make(map[string]struct{})}
	}
	if name != "" {
		c.flight.names[name] = struct{}{}
	}
	return c.flight, leader
}

func (c *nodeCache) refresh(ctx context.Context) error {
	c.mu.Lock()
	f, leader := c.beginRefresh("")
	c.mu.Unlock()
	return c.finishRefresh(ctx, f, leader)
}

func (c *nodeCache) finishRefresh(ctx context.Context, f *refresh, leader bool) error {
	if !leader {
		select {
		case <-f.done:
			return f.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, sourceTimeout)
	nodes, err := c.source.Nodes(readCtx)
	cancel()
	c.mu.Lock()
	if err == nil {
		c.nodes = nodes
		now := time.Now()
		for name, expires := range c.misses {
			if _, known := nodes.Hosts[name]; known || !now.Before(expires) {
				delete(c.misses, name)
			}
		}
		for name := range f.names {
			if _, known := nodes.Hosts[name]; !known {
				c.misses[name] = now.Add(negativeTTL)
			}
		}
	}
	f.err = err
	c.flight = nil
	close(f.done)
	c.mu.Unlock()
	if err != nil {
		slog.Warn("Tailscale refresh failed; retaining previous data", "error", err)
	} else {
		slog.Debug("Tailscale cache refreshed", "nodes", len(nodes.Hosts))
	}
	return err
}

func (c *nodeCache) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = c.refresh(ctx)
		case <-ctx.Done():
			return
		}
	}
}
