package main

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type sourceFunc func(context.Context) (*nodeMap, error)

func (f sourceFunc) Nodes(ctx context.Context) (*nodeMap, error) { return f(ctx) }

func testNodes() *nodeMap {
	return &nodeMap{Self: "server", Hosts: map[string][]netip.Addr{
		"server": {netip.MustParseAddr("100.64.0.1")},
		"freki":  {netip.MustParseAddr("100.64.0.2"), netip.MustParseAddr("fd7a:115c:a1e0::2")},
		"v6":     {netip.MustParseAddr("fd7a:115c:a1e0::3")},
		"empty":  {},
	}}
}

func TestCacheMissesAndReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		calls := 0
		next := testNodes()
		c := newCache(sourceFunc(func(context.Context) (*nodeMap, error) { calls++; return next, nil }))
		if err := c.refresh(ctx); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := c.lookup(ctx, "freki"); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 1 {
			t.Fatalf("known names refreshed: %d calls", calls)
		}
		_, _ = c.lookup(ctx, "new")
		_, _ = c.lookup(ctx, "new")
		if calls != 2 {
			t.Fatalf("repeated miss: %d calls, want 2", calls)
		}
		// A different miss must not invalidate the first name's negative cache.
		_, _ = c.lookup(ctx, "other")
		_, _ = c.lookup(ctx, "new")
		if calls != 3 {
			t.Fatalf("unrelated refresh discarded negative cache: %d", calls)
		}
		next = testNodes()
		next.Hosts["new"] = []netip.Addr{netip.MustParseAddr("100.64.0.4")}
		delete(next.Hosts, "freki")
		time.Sleep(negativeTTL)
		nodes, err := c.lookup(ctx, "new")
		if err != nil || len(nodes.Hosts["new"]) != 1 || calls != 4 {
			t.Fatalf("expired miss did not discover new host: nodes=%v err=%v calls=%d", nodes, err, calls)
		}
		if _, exists := nodes.Hosts["freki"]; exists {
			t.Fatal("successful refresh retained removed host")
		}
	})
}

func TestCacheFailureAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		fail := true
		c := newCache(sourceFunc(func(context.Context) (*nodeMap, error) {
			if fail {
				return nil, errors.New("offline")
			}
			return testNodes(), nil
		}))
		if _, err := c.lookup(ctx, "freki"); err == nil {
			t.Fatal("initial failure was hidden")
		}
		fail = false
		if _, err := c.lookup(ctx, "freki"); err != nil {
			t.Fatal(err)
		}
		fail = true
		time.Sleep(24 * time.Hour)
		if err := c.refresh(ctx); err == nil {
			t.Fatal("expected refresh failure")
		}
		if nodes, err := c.lookup(ctx, "freki"); err != nil || len(nodes.Hosts["freki"]) != 2 {
			t.Fatalf("stale known name lost: %v", err)
		}
		if _, err := c.lookup(ctx, "unknown"); err == nil {
			t.Fatal("failed refresh incorrectly confirmed absence")
		}
		if _, exists := c.misses["unknown"]; exists {
			t.Fatal("source error became a negative cache entry")
		}
	})
}

func TestCacheConcurrentRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		var calls atomic.Int32
		release := make(chan struct{})
		c := newCache(sourceFunc(func(context.Context) (*nodeMap, error) {
			calls.Add(1)
			<-release
			return testNodes(), nil
		}))
		c.nodes = testNodes()
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				if _, err := c.lookup(ctx, "unknown"); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Go(func() { _ = c.refresh(ctx) })
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("concurrent misses caused %d refreshes", got)
		}
		// This must complete while the source is still blocked.
		if _, err := c.lookup(ctx, "freki"); err != nil {
			t.Fatal(err)
		}
		close(release)
		wg.Wait()
		_, _ = c.lookup(ctx, "unknown")
		if got := calls.Load(); got != 1 {
			t.Fatalf("negative result was not shared: %d refreshes", got)
		}
	})
}

func TestPeriodicRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		c := newCache(sourceFunc(func(context.Context) (*nodeMap, error) { calls++; return testNodes(), nil }))
		_ = c.refresh(ctx)
		go c.run(ctx, 5*time.Minute)
		synctest.Wait()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if calls != 2 {
			t.Fatalf("got %d refreshes, want startup plus periodic", calls)
		}
		cancel()
		synctest.Wait()
		time.Sleep(5 * time.Minute)
		if calls != 2 {
			t.Fatal("refresh continued after shutdown")
		}
	})
}

func TestSourceTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCache(sourceFunc(func(ctx context.Context) (*nodeMap, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}))
		if err := c.refresh(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected timeout, got %v", err)
		}
	})
}
