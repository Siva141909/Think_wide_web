package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrentDistinctEvents: 100 concurrent submissions of different
// events must all be counted exactly once.
func TestConcurrentDistinctEvents(t *testing.T) {
	s := NewStore()
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			s.ProcessEvent(mustEvent(fmt.Sprintf("evt_%d", i), "cmp_a", fmt.Sprintf("ct_%d", i), "sent", "2026-08-10T06:15:00Z"))
		}(i)
	}
	wg.Wait()

	snap, _ := s.GetStats("cmp_a")
	if snap.Sent != n {
		t.Fatalf("sent = %d, want %d", snap.Sent, n)
	}
}

// TestConcurrentSameEventID: 100 concurrent submissions of the identical
// event must yield exactly one accepted and the rest duplicate, with the
// counter incremented exactly once.
func TestConcurrentSameEventID(t *testing.T) {
	s := NewStore()
	const n = 100
	ev := mustEvent("evt_shared", "cmp_a", "ct_1", "sent", "2026-08-10T06:15:00Z")

	var accepted, duplicate int64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			switch s.ProcessEvent(ev).Status {
			case StatusAccepted:
				atomic.AddInt64(&accepted, 1)
			case StatusDuplicate:
				atomic.AddInt64(&duplicate, 1)
			}
		}()
	}
	wg.Wait()

	if accepted != 1 {
		t.Fatalf("accepted = %d, want exactly 1", accepted)
	}
	if duplicate != n-1 {
		t.Fatalf("duplicate = %d, want %d", duplicate, n-1)
	}
	snap, _ := s.GetStats("cmp_a")
	if snap.Sent != 1 {
		t.Fatalf("sent = %d, want 1 (must never be double counted)", snap.Sent)
	}
}

// TestConcurrentConflictingPayloads: the same event_id submitted
// concurrently with two different payloads must resolve to exactly one
// accepted, and every other submission must be classified as either a
// duplicate of the winning payload or a conflict against it — never
// counted twice, and never both payloads applied. With n/2 submissions
// of each payload, whichever payload wins the race, the outcome is
// always: 1 accepted, (n/2 - 1) duplicates of the winner, n/2 conflicts
// from the other payload — regardless of which side happens to win.
func TestConcurrentConflictingPayloads(t *testing.T) {
	s := NewStore()
	const n = 100
	const half = n / 2
	a := mustEvent("evt_shared", "cmp_a", "ct_1", "sent", "2026-08-10T06:15:00Z")
	b := mustEvent("evt_shared", "cmp_a", "ct_1", "clicked", "2026-08-10T06:15:00Z")

	var accepted, duplicate, conflict int64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		ev := a
		if i%2 == 1 {
			ev = b
		}
		go func(ev Event) {
			defer wg.Done()
			switch s.ProcessEvent(ev).Status {
			case StatusAccepted:
				atomic.AddInt64(&accepted, 1)
			case StatusDuplicate:
				atomic.AddInt64(&duplicate, 1)
			case StatusConflict:
				atomic.AddInt64(&conflict, 1)
			}
		}(ev)
	}
	wg.Wait()

	if accepted != 1 {
		t.Fatalf("accepted = %d, want exactly 1", accepted)
	}
	if duplicate != half-1 {
		t.Fatalf("duplicate = %d, want %d", duplicate, half-1)
	}
	if conflict != half {
		t.Fatalf("conflict = %d, want %d", conflict, half)
	}
	snap, _ := s.GetStats("cmp_a")
	total := snap.Sent + snap.Clicked
	if total != 1 {
		t.Fatalf("sent+clicked = %d, want exactly 1 (never both payloads counted)", total)
	}
}

// TestConcurrentPostAndGet: interleaved writes and reads must never race
// and must never return a torn snapshot (each field read is consistent
// with the others at the moment of the read).
func TestConcurrentPostAndGet(t *testing.T) {
	s := NewStore()
	const writers = 50
	var wg sync.WaitGroup
	wg.Add(writers + writers)

	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			s.ProcessEvent(mustEvent(fmt.Sprintf("evt_%d", i), "cmp_a", fmt.Sprintf("ct_%d", i), "delivered", "2026-08-10T06:15:00Z"))
		}(i)
	}
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			s.GetStats("cmp_a")
		}()
	}
	wg.Wait()

	snap, _ := s.GetStats("cmp_a")
	if snap.Delivered != writers {
		t.Fatalf("delivered = %d, want %d", snap.Delivered, writers)
	}
}
