package main

import (
	"errors"
	"fmt"
	"time"
)

var validEventTypes = map[string]bool{
	"sent":      true,
	"delivered": true,
	"opened":    true,
	"clicked":   true,
}

// validateEvent checks an event against the fixed wire contract. All of
// event_id, campaign_id, contact_id, type and timestamp are required — the
// assignment calls out only metadata as optional, so silence on the others
// is read as "required". It returns the parsed timestamp so callers don't
// re-parse it.
func validateEvent(ev Event) (time.Time, error) {
	if ev.EventID == "" {
		return time.Time{}, errors.New("event_id is required")
	}
	if ev.CampaignID == "" {
		return time.Time{}, errors.New("campaign_id is required")
	}
	if ev.ContactID == "" {
		return time.Time{}, errors.New("contact_id is required")
	}
	if !validEventTypes[ev.Type] {
		return time.Time{}, fmt.Errorf("invalid type %q", ev.Type)
	}
	ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp: %v", err)
	}
	return ts, nil
}
