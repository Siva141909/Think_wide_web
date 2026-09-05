# campaign-events

Ingests campaign events from delivery providers and serves per-campaign stats for the dashboard. In-memory persistence, stdlib only.

## Requirements

- Go 1.22 or later (the `"POST /events"` routing pattern needs it). Check with `go version`.
- No third-party dependencies.

## Run

    go run .

Then, in another terminal:

    curl -s -X POST localhost:8080/events \
      -H 'Content-Type: application/json' \
      -d '[{"event_id":"evt_1","campaign_id":"cmp_summer_sale","contact_id":"ct_001","type":"delivered","timestamp":"2026-08-10T06:15:00Z"}]'

    curl -s localhost:8080/campaigns/cmp_summer_sale/stats

## Tests

    go test ./...
    go test -race -count=5 ./...

Includes unit tests (dedup/conflict/validation), concurrency tests under `-race` (concurrent distinct events, concurrent same-event_id, concurrent conflicting payloads, concurrent POST/GET), HTTP-level handler tests, and a regression test that replays the full `seed/events.json` and checks the resulting batch counts and per-campaign stats against independently-computed expected values.

## `POST /events`

Body must be a JSON array of events:

```json
{"event_id":"...","campaign_id":"...","contact_id":"...","type":"sent|delivered|opened|clicked","timestamp":"RFC3339","metadata":{"...":"..."}}
```

`event_id`, `campaign_id`, `contact_id`, `type`, and `timestamp` are required; `type` must exactly match one of the four values (case-sensitive); `timestamp` must parse as RFC3339. `metadata` is optional.

- A body that isn't valid JSON, or is valid JSON but not an array, gets **400** and nothing is processed.
- Otherwise the response is always **200**, and every element in the array is validated and applied independently — one bad element never blocks its siblings. The body classifies each element:

```json
{
  "accepted": 1, "duplicate": 1, "conflict": 1, "rejected": 1,
  "results": [
    {"index": 0, "event_id": "evt_1", "status": "accepted"},
    {"index": 1, "event_id": "evt_1", "status": "duplicate"},
    {"index": 2, "event_id": "evt_1", "status": "conflict", "reason": "event_id previously seen with a different payload"},
    {"index": 3, "event_id": "evt_2", "status": "rejected", "reason": "contact_id is required"}
  ]
}
```

- `accepted` — a new, valid event; counted.
- `duplicate` — same `event_id` seen before with an identical payload (a provider retry); not recounted.
- `conflict` — same `event_id` seen before with a *different* payload; the first one received wins, this one is not applied.
- `rejected` — failed validation (missing required field, unknown/mis-cased `type`, or an unparseable `timestamp`); never applied, and never reserves its `event_id` for dedup purposes.

## `GET /campaigns/{campaign_id}/stats`

Returns **200** with the campaign's counters — e.g. after the single `delivered` event posted in the Run example above:

```json
{"campaign_id":"cmp_summer_sale","sent":0,"delivered":1,"opened":0,"unique_opens":0,"clicked":0}
```

`opened` is the raw count of accepted `opened` events; `unique_opens` is the number of distinct contacts among them for that campaign.

Returns **404** if `campaign_id` has never had an accepted event (there's no campaign registry, so "unknown" and "no activity yet" are indistinguishable).

## Seed data

`seed/events.json` is one day of realistic provider traffic: 235 events including retries, conflicting duplicates, and malformed records (missing fields, unknown/mis-cased `type`, bad timestamps). The service processes all of it without crashing; `seed_test.go` replays it and checks the resulting classification counts and per-campaign stats against independently-computed expected values.

To load it manually:

    curl -s -X POST localhost:8080/events \
      -H 'Content-Type: application/json' \
      --data-binary @seed/events.json
