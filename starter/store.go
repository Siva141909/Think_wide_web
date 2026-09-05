package main

import "sync"

// Store holds all in-memory campaign state. Three lock domains are used,
// and a goroutine never holds more than one at a time:
//
//   - dedupMu    guards seenEvents (event_id -> fingerprint reservation).
//   - campaignsMu guards the campaigns map's key set (creating a new
//     campaign entry).
//   - each campaignState's own mu guards that campaign's counters.
//
// Because no goroutine ever acquires a second lock while holding the
// first, there is no lock-ordering cycle and therefore no deadlock,
// regardless of how the three are interleaved across requests.
type Store struct {
	campaignsMu sync.RWMutex
	campaigns   map[string]*campaignState

	dedupMu    sync.Mutex
	seenEvents map[string]fingerprint
}

func NewStore() *Store {
	return &Store{
		campaigns:  make(map[string]*campaignState),
		seenEvents: make(map[string]fingerprint),
	}
}

type campaignState struct {
	mu             sync.RWMutex
	Sent           int
	Delivered      int
	Opened         int
	Clicked        int
	openedContacts map[string]struct{}
}

type StatsSnapshot struct {
	CampaignID  string
	Sent        int
	Delivered   int
	Opened      int
	UniqueOpens int
	Clicked     int
}

type reserveResult int

const (
	reserveAccepted reserveResult = iota
	reserveDuplicate
	reserveConflict
)

// reserve is the single atomic check-and-set on an event_id: it decides,
// in one uninterrupted critical section, whether this call is the first
// to see event_id, a duplicate of an identical payload, or a conflict
// with a different payload. Exactly one concurrent caller for a given new
// event_id can ever receive reserveAccepted.
func (s *Store) reserve(eventID string, fp fingerprint) reserveResult {
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()

	existing, ok := s.seenEvents[eventID]
	if !ok {
		s.seenEvents[eventID] = fp
		return reserveAccepted
	}
	if existing.Equal(fp) {
		return reserveDuplicate
	}
	return reserveConflict
}

// campaignFor returns the state for campaignID, creating it if this is
// the first time it's been seen. The read path (common case) only takes
// campaignsMu.RLock(); creation briefly upgrades to a write lock via a
// double-checked lookup, never holding both at once.
func (s *Store) campaignFor(campaignID string) *campaignState {
	s.campaignsMu.RLock()
	cs, ok := s.campaigns[campaignID]
	s.campaignsMu.RUnlock()
	if ok {
		return cs
	}

	s.campaignsMu.Lock()
	defer s.campaignsMu.Unlock()
	if cs, ok = s.campaigns[campaignID]; ok {
		return cs
	}
	cs = &campaignState{openedContacts: make(map[string]struct{})}
	s.campaigns[campaignID] = cs
	return cs
}

func (s *Store) lookupCampaign(campaignID string) (*campaignState, bool) {
	s.campaignsMu.RLock()
	defer s.campaignsMu.RUnlock()
	cs, ok := s.campaigns[campaignID]
	return cs, ok
}

func (cs *campaignState) apply(ev Event) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	switch ev.Type {
	case "sent":
		cs.Sent++
	case "delivered":
		cs.Delivered++
	case "opened":
		cs.Opened++
		cs.openedContacts[ev.ContactID] = struct{}{}
	case "clicked":
		cs.Clicked++
	}
}

func (cs *campaignState) snapshot(campaignID string) StatsSnapshot {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return StatsSnapshot{
		CampaignID:  campaignID,
		Sent:        cs.Sent,
		Delivered:   cs.Delivered,
		Opened:      cs.Opened,
		UniqueOpens: len(cs.openedContacts),
		Clicked:     cs.Clicked,
	}
}

// ProcessEvent validates, deduplicates and (if accepted) applies a single
// event, returning the outcome for the batch response.
func (s *Store) ProcessEvent(ev Event) ItemResult {
	ts, err := validateEvent(ev)
	if err != nil {
		return ItemResult{EventID: ev.EventID, Status: StatusRejected, Reason: err.Error()}
	}

	fp := buildFingerprint(ev, ts)
	switch s.reserve(ev.EventID, fp) {
	case reserveDuplicate:
		return ItemResult{EventID: ev.EventID, Status: StatusDuplicate}
	case reserveConflict:
		return ItemResult{EventID: ev.EventID, Status: StatusConflict, Reason: "event_id previously seen with a different payload"}
	default:
		cs := s.campaignFor(ev.CampaignID)
		cs.apply(ev)
		return ItemResult{EventID: ev.EventID, Status: StatusAccepted}
	}
}

// GetStats returns a consistent snapshot of a campaign's counters, or
// false if campaign_id has never had an accepted event.
//
// This is intentionally asymmetric with campaignFor: there is no
// campaign-registration endpoint, so POST /events is permissive by
// design — any non-empty campaign_id creates state the moment its first
// event is accepted (campaignFor, called only from the accepted branch
// of ProcessEvent) — while this read path treats "no state yet" as
// "unknown campaign" (404), since without a registry the two are
// indistinguishable. A campaign_id that has only ever appeared in
// rejected, duplicate, or conflicting events (never one accepted) 404s
// here until its first accepted event; that's consistent with those
// events having produced no observable state to report. Crucially, this
// read path (lookupCampaign) never creates state and never interacts
// with event_id reservation (seenEvents) — POST /events has no 404
// branch anywhere, so an event can never be "reserved" by a request that
// itself then reports the campaign as unknown.
func (s *Store) GetStats(campaignID string) (StatsSnapshot, bool) {
	cs, ok := s.lookupCampaign(campaignID)
	if !ok {
		return StatsSnapshot{}, false
	}
	return cs.snapshot(campaignID), true
}
