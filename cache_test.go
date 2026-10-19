package main

import (
	"testing"
	"time"
)

func TestMemoExpiry(t *testing.T) {
	now := time.Now()
	m := NewMemo(1024)
	m.now = func() time.Time { return now }

	m.Put("k", "v", time.Minute, 10)
	if v, ok := m.Get("k"); !ok || v.(string) != "v" {
		t.Fatal("expected cache hit")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := m.Get("k"); ok {
		t.Fatal("expected expiry")
	}
	if m.Len() != 0 {
		t.Fatalf("expired entry should be dropped, len=%d", m.Len())
	}
}

func TestMemoByteBound(t *testing.T) {
	m := NewMemo(100)
	for i := 0; i < 10; i++ {
		m.Put(string(rune('a'+i)), i, time.Hour, 30)
	}
	if m.bytes > 100 {
		t.Fatalf("cache exceeded byte bound: %d", m.bytes)
	}
	if m.Len() == 0 {
		t.Fatal("cache evicted everything")
	}
}

func TestMemoRejectsOversizedEntry(t *testing.T) {
	m := NewMemo(100)
	m.Put("big", "x", time.Hour, 500)
	if m.Len() != 0 {
		t.Fatal("oversized entry should not be stored")
	}
}
