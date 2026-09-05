package main

import "testing"

func mustEvent(id, campaign, contact, typ, ts string) Event {
	return Event{EventID: id, CampaignID: campaign, ContactID: contact, Type: typ, Timestamp: ts}
}

func TestExactDuplicateIsNoOp(t *testing.T) {
	s := NewStore()
	ev := mustEvent("evt_1", "cmp_a", "ct_1", "delivered", "2026-08-10T06:15:00Z")

	r1 := s.ProcessEvent(ev)
	r2 := s.ProcessEvent(ev)

	if r1.Status != StatusAccepted {
		t.Fatalf("first submission: got %q, want accepted", r1.Status)
	}
	if r2.Status != StatusDuplicate {
		t.Fatalf("second submission: got %q, want duplicate", r2.Status)
	}
	snap, _ := s.GetStats("cmp_a")
	if snap.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1 (duplicate must not double count)", snap.Delivered)
	}
}

func TestDuplicateIsIdenticalRegardlessOfTimestampFormatting(t *testing.T) {
	s := NewStore()
	a := mustEvent("evt_1", "cmp_a", "ct_1", "delivered", "2026-08-10T06:15:00Z")
	b := mustEvent("evt_1", "cmp_a", "ct_1", "delivered", "2026-08-10T06:15:00.000+00:00")

	s.ProcessEvent(a)
	r := s.ProcessEvent(b)
	if r.Status != StatusDuplicate {
		t.Fatalf("equivalent instant in a different format: got %q, want duplicate", r.Status)
	}
}

func TestConflictingPayloadUnderSameEventID(t *testing.T) {
	s := NewStore()
	a := mustEvent("evt_1", "cmp_a", "ct_1", "sent", "2026-08-10T06:15:00Z")
	b := mustEvent("evt_1", "cmp_a", "ct_1", "clicked", "2026-08-10T06:15:00Z")

	r1 := s.ProcessEvent(a)
	r2 := s.ProcessEvent(b)

	if r1.Status != StatusAccepted {
		t.Fatalf("first payload: got %q, want accepted", r1.Status)
	}
	if r2.Status != StatusConflict {
		t.Fatalf("second, differing payload: got %q, want conflict", r2.Status)
	}
	snap, _ := s.GetStats("cmp_a")
	if snap.Sent != 1 || snap.Clicked != 0 {
		t.Fatalf("first-write-wins violated: sent=%d clicked=%d, want sent=1 clicked=0", snap.Sent, snap.Clicked)
	}
}

func TestInvalidEventDoesNotReserveEventID(t *testing.T) {
	s := NewStore()
	bad := mustEvent("evt_1", "cmp_a", "ct_1", "delivered", "not-a-time")
	good := mustEvent("evt_1", "cmp_a", "ct_1", "delivered", "2026-08-10T06:15:00Z")

	r1 := s.ProcessEvent(bad)
	if r1.Status != StatusRejected {
		t.Fatalf("bad timestamp: got %q, want rejected", r1.Status)
	}
	r2 := s.ProcessEvent(good)
	if r2.Status != StatusAccepted {
		t.Fatalf("valid resend after a rejected attempt: got %q, want accepted (rejected must not reserve event_id)", r2.Status)
	}
}

func TestRequiredFieldsRejected(t *testing.T) {
	base := mustEvent("evt_1", "cmp_a", "ct_1", "delivered", "2026-08-10T06:15:00Z")
	cases := []struct {
		name string
		mut  func(Event) Event
	}{
		{"missing event_id", func(e Event) Event { e.EventID = ""; return e }},
		{"missing campaign_id", func(e Event) Event { e.CampaignID = ""; return e }},
		{"missing contact_id", func(e Event) Event { e.ContactID = ""; return e }},
		{"unknown type", func(e Event) Event { e.Type = "spam_report"; return e }},
		{"wrong-case type", func(e Event) Event { e.Type = "OPENED"; return e }},
		{"unparseable timestamp", func(e Event) Event { e.Timestamp = "10-08-2026 09:00"; return e }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := NewStore()
			ev := c.mut(base)
			r := s.ProcessEvent(ev)
			if r.Status != StatusRejected {
				t.Fatalf("%s: got %q, want rejected", c.name, r.Status)
			}
		})
	}
}

func TestOpenedVsUniqueOpens(t *testing.T) {
	s := NewStore()
	s.ProcessEvent(mustEvent("e1", "cmp_a", "ct_1", "opened", "2026-08-10T06:00:00Z"))
	s.ProcessEvent(mustEvent("e2", "cmp_a", "ct_1", "opened", "2026-08-10T07:00:00Z"))
	s.ProcessEvent(mustEvent("e3", "cmp_a", "ct_1", "opened", "2026-08-10T08:00:00Z"))

	snap, _ := s.GetStats("cmp_a")
	if snap.Opened != 3 {
		t.Fatalf("opened = %d, want 3", snap.Opened)
	}
	if snap.UniqueOpens != 1 {
		t.Fatalf("unique_opens = %d, want 1 (same contact, three opens)", snap.UniqueOpens)
	}
}

func TestUniqueOpensCountsOncePerCampaignIndependently(t *testing.T) {
	s := NewStore()
	s.ProcessEvent(mustEvent("e1", "cmp_a", "ct_1", "opened", "2026-08-10T06:00:00Z"))
	s.ProcessEvent(mustEvent("e2", "cmp_b", "ct_1", "opened", "2026-08-10T07:00:00Z"))

	snapA, _ := s.GetStats("cmp_a")
	snapB, _ := s.GetStats("cmp_b")
	if snapA.UniqueOpens != 1 || snapB.UniqueOpens != 1 {
		t.Fatalf("cmp_a unique_opens=%d cmp_b unique_opens=%d, want 1 and 1 (same contact counts once in each campaign)", snapA.UniqueOpens, snapB.UniqueOpens)
	}
}

func TestOpenedDoesNotRequirePriorDelivered(t *testing.T) {
	s := NewStore()
	// opened arrives before delivered for the same message — must not be rejected.
	r1 := s.ProcessEvent(mustEvent("e1", "cmp_a", "ct_1", "opened", "2026-08-10T06:00:00Z"))
	r2 := s.ProcessEvent(mustEvent("e2", "cmp_a", "ct_1", "delivered", "2026-08-10T07:00:00Z"))

	if r1.Status != StatusAccepted || r2.Status != StatusAccepted {
		t.Fatalf("out-of-order opened/delivered must both be accepted, got %q and %q", r1.Status, r2.Status)
	}
}

func TestUnknownCampaignStatsIs404(t *testing.T) {
	s := NewStore()
	_, ok := s.GetStats("does-not-exist")
	if ok {
		t.Fatalf("expected ok=false for a campaign_id never seen")
	}
}

// TestRejectedEventNeitherCreatesCampaignNorReservesEventID is the
// empirical check for the concern raised in review: that an event_id
// could be "poisoned" by a request that never actually gets applied
// because the campaign turns out to be unknown. It can't happen here —
// campaignFor (called only after an event is accepted) always creates
// the campaign, and GetStats's 404 path never runs during POST
// processing — but this proves it end to end rather than relying on
// reading the code.
func TestRejectedEventNeitherCreatesCampaignNorReservesEventID(t *testing.T) {
	s := NewStore()

	bad := mustEvent("evt_1", "cmp_new", "", "delivered", "2026-08-10T06:15:00Z") // missing contact_id
	r1 := s.ProcessEvent(bad)
	if r1.Status != StatusRejected {
		t.Fatalf("invalid event: got %q, want rejected", r1.Status)
	}
	if _, ok := s.GetStats("cmp_new"); ok {
		t.Fatalf("campaign must not exist after only a rejected event (no accepted event yet)")
	}

	good := mustEvent("evt_1", "cmp_new", "ct_1", "delivered", "2026-08-10T06:15:00Z")
	r2 := s.ProcessEvent(good)
	if r2.Status != StatusAccepted {
		t.Fatalf("valid retry of the same event_id after a prior rejection: got %q, want accepted (rejection must not reserve event_id)", r2.Status)
	}

	snap, ok := s.GetStats("cmp_new")
	if !ok {
		t.Fatalf("campaign must exist immediately after its first accepted event")
	}
	if snap.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1", snap.Delivered)
	}
}
