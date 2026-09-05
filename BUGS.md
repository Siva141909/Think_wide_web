# BUGS.md — Part 3 debugging findings

All four bugs below were found in `debugging/main.go`. Each is fixed with the smallest possible change; no restructuring, no output-format changes beyond what each fix requires.

Baseline reproduced first, unmodified, per the prescribed command:

```
go run . events.jsonl > actual.txt
diff actual.txt expected_output.txt
```

This showed every campaign's `sent`/`delivered`/`opened`/`clicked` inflated, `unique_opens` far too low, an unexpected extra `2026-08-08` bucket, and non-identical output across repeated runs (confirmed by running 5 times and diffing pairwise — no two runs agreed). `go run -race . events.jsonl` additionally reported 8 `DATA RACE` warnings, all inside `apply()`.

---

## Bug 1 — Duplicate `event_id`s are only caught within a batch, not across the whole file

**What it is:** `processBatch` created a fresh `seen := make(map[string]bool)` on every call (main.go, inside `processBatch`, before the fix). Input is read and dispatched in batches of 200 lines. Any duplicate `event_id` whose two occurrences land in *different* batches was never detected — each batch's dedup map started empty, so the second occurrence just looked "new" again.

**Why it's wrong:** the file's own header comment states the requirement plainly: "Each event_id must be counted at most once **across the whole file**." Providers retry, and nothing guarantees a retry lands in the same 200-line window as the original — in this 20,000-line file, most duplicate pairs are far enough apart to land in different batches, so this bug alone accounted for the large inflation in every counter.

**Fix:** moved `seen` out of `processBatch` and made it a package-level map (alongside `stats` and `openedBy`, which already use this same pattern and are documented as main-goroutine-only). `processBatch` no longer re-initializes it, so a duplicate is caught no matter which batch it falls in.

```go
var seen = map[string]bool{}
...
func processBatch(events []Event) {
	unique := make([]Event, 0, len(events))
	for _, ev := range events {
		if seen[ev.EventID] {
			continue
		}
		seen[ev.EventID] = true
		...
```

**How verified:** wrote an independent Python reference implementation from scratch (global dedup, no batching), ran it over `events.jsonl`, and its output matched `expected_output.txt` byte-for-byte — confirming that a whole-file dedup is what the expected output actually assumes. After the fix, `sent`/`delivered`/`opened`/`clicked` for all five campaigns matched `expected_output.txt` exactly (verified together with bugs 2–4 below, since all four needed fixing before any run could match).

---

## Bug 2 — `unique_opens` is deduplicated by contact alone, not by (campaign, contact)

**What it is:** `openedBy` was a single global `map[string]bool` keyed only by `ev.ContactID`. Once a contact was recorded as having opened *any* campaign, they could never increment `UniqueOpens` again for a *different* campaign.

**Why it's wrong:** the spec is explicit: "`unique_opens` = the number of distinct contacts that opened, per campaign. ... The same contact opening in two campaigns counts once *in each*." The single global set collapses cross-campaign activity, undercounting `unique_opens` for every campaign a shared contact touched (this is why the baseline's `unique_opens` numbers were roughly half of expected, e.g. `549` vs. expected `942` for `cmp_A`).

**Fix:** scoped the dedup key to `(campaign_id, contact_id)`:

```go
type openKey struct{ campaign, contact string }

var openedBy = map[openKey]bool{}
...
case "opened":
	key := openKey{ev.CampaignID, ev.ContactID}
	if !openedBy[key] {
		openedBy[key] = true
		cs.UniqueOpens++
	}
```

**How verified:** same independent Python reference (keyed the open-tracking set by `(campaign_id, contact_id)`) reproduced `expected_output.txt`'s `unique_opens` values exactly for all five campaigns after this change was mirrored in the reference.

---

## Bug 3 — Daily delivered buckets use the machine's local timezone, not UTC

**What it is:** `track()` computed the bucket date with `ev.Timestamp.Local().Format("2006-01-02")` instead of `.UTC()`.

**Why it's wrong:** the spec states "Daily `delivered` buckets use the event's UTC date. The `timestamp` field is UTC." Using `.Local()` shifts the bucket date by whatever offset the running machine's timezone happens to be. On this machine (`IST`, `UTC+5:30`), events timestamped late in the UTC day (roughly 18:30 UTC onward) shifted into the *next* local day — which is exactly why the baseline output had a spurious extra `2026-08-08` bucket for every campaign, one day past the file's real Aug 1–7 range, and why the per-day counts for Aug 7 and Aug 8 didn't match `expected_output.txt`'s Aug 1–7-only totals. This bug is also a portability defect independent of correctness: the buggy program would produce *different* wrong output on a machine in a different timezone.

**Fix:** one-word change, `.Local()` → `.UTC()`:

```go
case "delivered":
	day := ev.Timestamp.UTC().Format("2006-01-02")
	cs.DailyDelivered[day]++
```

**How verified:** confirmed the machine's local timezone (`IST`, `+05:30`) via `date +"%Z %z"`, confirming the causal mechanism. After the fix, no `2026-08-08` bucket appears, and every campaign's seven daily-delivered lines (Aug 1–7) match `expected_output.txt` exactly.

---

## Bug 4 — Unsynchronized concurrent counter increments in `apply()` (data race)

**What it is:** `apply()` runs on 8 worker goroutines (`processBatch`'s worker pool) and does plain, unguarded increments — `cs.Sent++`, `cs.Delivered++`, `cs.Opened++`, `cs.Clicked++` — on a `*CampaignStats` shared across all workers whenever multiple events in the same batch belong to the same campaign (very common with only 5 campaigns and batches of 200). `++` on a plain `int` is a non-atomic read-modify-write; two goroutines interleaving this on the same field silently lose one of the two increments.

**Why it's wrong:** the spec requires "Output must be identical across runs on the same input." Running the unmodified program 5 times and diffing every pair showed every single run disagreeing with every other — sometimes by 1, sometimes by more, in `sent`/`delivered`/`opened`/`clicked`, varying campaign by campaign and run by run, which is the signature of lost updates under a race rather than a systematic logic error. `go run -race . events.jsonl` confirmed this directly: 8 `DATA RACE` warnings, every one pointing at main.go's `apply()` (the `cs.Sent++`/`cs.Delivered++`/`cs.Opened++`/`cs.Clicked++` lines), with paired reads/writes from two different worker goroutines each time.

**Fix:** added a `sync.Mutex` to `CampaignStats` (the `sync` package was already imported for `sync.WaitGroup`, so no new import) and locked around the increments in `apply()`:

```go
type CampaignStats struct {
	mu             sync.Mutex // guards the counters below; apply() runs on worker goroutines
	Sent           int
	...
}
...
func apply(ev Event) {
	cs := stats[ev.CampaignID]
	cs.mu.Lock()
	defer cs.mu.Unlock()
	switch ev.Type {
	...
```

This is deliberately a per-campaign lock, not a coarser one: `track()`'s map operations (`openedBy`, `stats` creation) already run single-threaded before workers start, so only `apply()`'s counter increments needed protection, and locking per-`*CampaignStats` avoids serializing workers that happen to be updating different campaigns.

**How verified:**
- `go run -race . events.jsonl` after the fix: exit code 0, zero `DATA RACE` warnings (down from 8), repeated 3 times.
- `go run . events.jsonl` (no race flag) run 15 times in a row: every run's output diffed identically against `expected_output.txt` — full determinism confirmed, not just "looked fine once."

---

## Combined final verification (all four fixes applied together)

```
$ go run -race . events.jsonl > actual.txt
$ diff actual.txt expected_output.txt
(empty — exact match)
```

- `go vet ./...` and `go build ./...`: clean.
- `go run -race . events.jsonl`: exit 0, no race warnings, output identical to `expected_output.txt`.
- `go run . events.jsonl` run 15 times consecutively: all 15 outputs identical to `expected_output.txt` and to each other.
- Independent Python reference implementation (global dedup, `(campaign, contact)`-scoped unique-opens, UTC bucketing) matches `expected_output.txt` exactly, cross-checking that the fixes encode the right semantics and not just the right output on this one file/run.

Diff against the original file is minimal and confined to exactly the four bugs: one struct field added (`mu sync.Mutex`), one map moved from local to package scope, one map's key type changed from `string` to a small struct, one method call changed (`.Local()` → `.UTC()`), and two lines added to `apply()` (lock/defer-unlock). No renaming, no restructuring, no behavior changed beyond what each bug required.
