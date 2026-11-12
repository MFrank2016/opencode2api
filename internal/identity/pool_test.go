package identity

import (
	"testing"
	"time"
)

func TestPoolStableWithinTTLAndRementsAfter(t *testing.T) {
	now := time.Unix(1000, 0)
	pool := NewPool(PoolOptions{MaxEntries: 4, TTL: time.Minute, Now: func() time.Time { return now }})

	first := pool.Project("scope", "ip|ua")
	if again := pool.Project("scope", "ip|ua"); again != first {
		t.Fatalf("project changed within TTL: %q -> %q", first, again)
	}
	now = now.Add(2 * time.Minute)
	if later := pool.Project("scope", "ip|ua"); later == first {
		t.Fatal("project was not re-minted after the TTL")
	}
}

func TestPoolSeparatesCallersAndClients(t *testing.T) {
	now := time.Unix(1000, 0)
	pool := NewPool(PoolOptions{MaxEntries: 8, TTL: time.Minute, Now: func() time.Time { return now }})

	base := pool.Project("scope-a", "ip1|ua1")
	if other := pool.Project("scope-b", "ip1|ua1"); other == base {
		t.Fatal("two callers shared one project")
	}
	if other := pool.Project("scope-a", "ip2|ua1"); other == base {
		t.Fatal("two clients shared one project")
	}
	if other := pool.Project("scope-a", "ip1|ua2"); other == base {
		t.Fatal("two user-agent families shared one project")
	}
}

func TestPoolEvictsOldestBeyondCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	pool := NewPool(PoolOptions{MaxEntries: 2, TTL: time.Hour, Now: func() time.Time { return now }})

	first := pool.Project("scope", "a")
	pool.Project("scope", "b")
	pool.Project("scope", "c") // evicts "a"
	if again := pool.Project("scope", "a"); again == first {
		t.Fatal("evicted entry was not re-minted")
	}
}

func TestNilPoolReturnsEmpty(t *testing.T) {
	var pool *Pool
	if got := pool.Project("scope", "client"); got != "" {
		t.Fatalf("nil pool project = %q, want empty", got)
	}
}
