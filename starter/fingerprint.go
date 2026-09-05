package main

import (
	"reflect"
	"time"
)

// fingerprint is the canonical representation of an event's payload, used
// to decide whether a repeated event_id is a true retry (identical
// payload) or a conflict (different payload under the same id). Two
// events are "identical" iff every field matches after normalization to
// its parsed, decoded form — not its raw JSON bytes:
//
//   - Timestamp is compared as an instant via time.Time.Equal, not as a
//     string. RFC3339 defines "...Z" and "...+00:00", and a timestamp
//     with or without trailing zero sub-second digits, as encodings of
//     the same instant — treating those as a "different payload" would
//     be a false conflict manufactured by string formatting, not a real
//     difference in what happened. This is not the same kind of
//     leniency as, say, case-folding `type`: `type` is a closed,
//     five-way-enumerated contract field where "OPENED" is simply
//     outside the contract (see validate.go), whereas timestamp format
//     variance is exactly what the RFC3339 spec itself calls equivalent.
//   - Metadata is compared with reflect.DeepEqual after normalizing nil
//     to an empty map — again comparing the decoded map, not raw JSON
//     text, so key order in the original request never matters (JSON
//     objects are unordered by spec).
//
// In practice a provider retry resends the same serialized bytes, so
// this distinction rarely bites — but where it does, it keeps the
// definition of "identical" aligned with the wire contract's own
// semantics rather than with incidental serialization details.
type fingerprint struct {
	CampaignID string
	ContactID  string
	Type       string
	Timestamp  time.Time
	Metadata   map[string]string
}

func buildFingerprint(ev Event, ts time.Time) fingerprint {
	md := ev.Metadata
	if md == nil {
		md = map[string]string{}
	}
	return fingerprint{
		CampaignID: ev.CampaignID,
		ContactID:  ev.ContactID,
		Type:       ev.Type,
		Timestamp:  ts.UTC(),
		Metadata:   md,
	}
}

func (a fingerprint) Equal(b fingerprint) bool {
	if a.CampaignID != b.CampaignID || a.ContactID != b.ContactID || a.Type != b.Type {
		return false
	}
	if !a.Timestamp.Equal(b.Timestamp) {
		return false
	}
	return reflect.DeepEqual(a.Metadata, b.Metadata)
}
