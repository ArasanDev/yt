// Bounded in-memory memo used by the long-lived MCP server.
//
// The point is to stop the three-tools-three-player-fetches pattern, not to be
// a general-purpose cache: entries are byte-bounded and evicted oldest-expiry
// first.
package youtube

import (
	"sync"
	"time"
)

type memoEntry struct {
	value   any
	expires time.Time
	size    int
}

type Memo struct {
	mu       sync.Mutex
	entries  map[string]memoEntry
	bytes    int
	maxBytes int
	now      func() time.Time
}

func NewMemo(maxBytes int) *Memo {
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	return &Memo{entries: make(map[string]memoEntry), maxBytes: maxBytes, now: time.Now}
}

func (m *Memo) Get(key string) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, false
	}
	if m.now().After(e.expires) {
		delete(m.entries, key)
		m.bytes -= e.size
		return nil, false
	}
	return e.value, true
}

func (m *Memo) Put(key string, value any, ttl time.Duration, size int) {
	if size <= 0 {
		size = 1
	}
	if size > m.maxBytes {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.entries[key]; ok {
		m.bytes -= old.size
	}
	m.entries[key] = memoEntry{value: value, expires: m.now().Add(ttl), size: size}
	m.bytes += size
	for m.bytes > m.maxBytes {
		if !m.evictOldestLocked() {
			break
		}
	}
}

func (m *Memo) evictOldestLocked() bool {
	var key string
	var oldest time.Time
	first := true
	for k, e := range m.entries {
		if first || e.expires.Before(oldest) {
			key, oldest, first = k, e.expires, false
		}
	}
	if first {
		return false
	}
	m.bytes -= m.entries[key].size
	delete(m.entries, key)
	return true
}

func (m *Memo) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok {
		m.bytes -= e.size
		delete(m.entries, key)
	}
}

func (m *Memo) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
