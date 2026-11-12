package identity

import (
	"container/list"
	"sync"
	"time"
)

// PoolOptions configures a project-identity pool. Zero values select the
// defaults: 2000 entries and a 30-minute TTL.
type PoolOptions struct {
	MaxEntries int
	TTL        time.Duration
	// Now is the clock used for TTL checks. Tests substitute it; production
	// leaves it nil for time.Now.
	Now func() time.Time
}

// Pool keeps one stable project id per (caller scope, client bucket) for a
// TTL. The upstream free tier ledgers quota per project id and egress, and a
// real CLI keeps one project hash per workspace; a project that changes on
// every request is both implausible and hostile to prompt caching. The pool is
// bounded and LRU-ordered so a flood of distinct clients cannot grow it
// without limit.
//
// A nil Pool is valid and always returns "" (the caller then mints a random
// project, the historical behavior).
type Pool struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	max     int
	ttl     time.Duration
	now     func() time.Time
}

type poolEntry struct {
	key     string
	project string
	minted  time.Time
}

// NewPool builds a pool from options, applying the defaults.
func NewPool(options PoolOptions) *Pool {
	if options.MaxEntries <= 0 {
		options.MaxEntries = 2000
	}
	if options.TTL <= 0 {
		options.TTL = 30 * time.Minute
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Pool{
		entries: make(map[string]*list.Element, options.MaxEntries),
		order:   list.New(),
		max:     options.MaxEntries,
		ttl:     options.TTL,
		now:     options.Now,
	}
}

// Project returns the stable project id for one (scope, clientKey) pair,
// minting or re-minting it as needed. clientKey is the gateway's client
// fingerprint (IP + user-agent family); scope is the authenticated caller.
func (p *Pool) Project(scope, clientKey string) string {
	if p == nil {
		return ""
	}
	key := scope + "\x00" + clientKey
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if element, ok := p.entries[key]; ok {
		entry := element.Value.(*poolEntry)
		if now.Sub(entry.minted) < p.ttl {
			p.order.MoveToFront(element)
			return entry.project
		}
		p.order.Remove(element)
		delete(p.entries, key)
	}
	entry := &poolEntry{key: key, project: RandomProjectID(), minted: now}
	p.entries[key] = p.order.PushFront(entry)
	for p.order.Len() > p.max {
		oldest := p.order.Back()
		if oldest == nil {
			break
		}
		p.order.Remove(oldest)
		delete(p.entries, oldest.Value.(*poolEntry).key)
	}
	return entry.project
}
