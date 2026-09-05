# NOTES

## Part 1 — Read and think first

### Given facts (stated by the brief, not decisions we made)

- Four event types: `sent`, `delivered`, `opened`, `clicked`.
- The event payload shape is fixed (the provider contract): `event_id`, `campaign_id`, `contact_id`, `type`, `timestamp`, optional `metadata`. The brief explicitly calls out only `metadata` as optional.
- `event_id` is assigned by the provider and "identifies one event."
- `timestamp` is when the event happened at the provider, not when it reached us.
- Providers retry: the same event can arrive more than once, and duplicates must not double count.
- Events arrive late and out of order — an `opened` can legitimately arrive before the `delivered` for the same message.
- Volume today: a few thousand events/day across ~50 active campaigns, growing.
- Storage is our choice (SQLite or in-memory); no frontend required; no auth/deployment work wanted.

### My interpretation

Relay's service sits at a trust boundary: it receives webhook traffic from third-party providers it doesn't control, who resend on any non-2xx and sometimes "anyway," and it must turn that into stable, non-double-counted per-campaign numbers for a live dashboard. The two operations — ingest (`POST /events`) and query (`GET /campaigns/{id}/stats`) — have different correctness pressures: ingest must be safe against untrusted, possibly-malformed, possibly-repeated input; query must return a consistent snapshot cheaply. Because providers can send both exact retries and, per the seed data, occasional conflicting payloads under a reused `event_id`, "the same event" turned out to need two distinct answers (retry vs. conflict), not one.

### Decisions we made (our calls — not stated by the brief)

These were argued through in the design review and are implemented as described:

1. **Event identity** = `event_id` alone. Identical payload (compared on parsed/decoded fields, including `metadata`) → `duplicate`, no state change. Different payload under the same `event_id` → `conflict`, first valid event wins, the conflicting one is never applied. An event that fails validation never reserves its `event_id` (a corrected resend later is treated as fresh).
2. **`contact_id` is required**, alongside `event_id`, `campaign_id`, `type`, `timestamp`. The brief calls out only `metadata` as optional; that silence next to an explicit optionality note for `metadata` was read as "required by omission," not confirmed by an explicit statement. This is the one required-field decision that's a genuine inference rather than a direct reading — flagged again under Ambiguities below.
3. **`type` must exactly match one of the four documented values, case-sensitively.** No normalization (e.g. `"OPENED"` is rejected, not folded to `"opened"`) — the four types are an enumerated, fixed contract, so tolerating variants would be guessing at provider intent rather than honoring the contract.
4. **Timestamp identity is compared by parsed instant, not raw string** (`"...Z"` vs `"...+00:00"`, or differing sub-second precision, are the same instant per RFC3339 and are treated as identical payloads). This is different in kind from decision 3: RFC3339 itself defines those encodings as equivalent, so this isn't invented leniency.
5. **No causal/ordering validation between event types** (e.g. no requirement that `delivered` precede `opened`). The brief states out-of-order arrival as a normal fact of the business, not a defect to engineer around.
6. **Partial batch acceptance.** A request body that isn't valid JSON, or is valid JSON but not an array, is rejected outright (400), nothing processed. Once the array itself parses, every element is validated and applied independently — one malformed element never blocks its siblings.
7. **`POST /events` always returns 200** for a successfully parsed array, regardless of mixed outcomes, with a body classifying every item as `accepted` / `duplicate` / `conflict` / `rejected`. Chosen over per-outcome status codes so a provider's retry logic can't misread a partially-rejected batch as a total failure and resend already-accepted items.
8. **Both `opened` (raw, deduplicated count) and `unique_opens` (distinct contacts, per campaign) are exposed**, since the brief explicitly poses "what does 'how many opened' mean" as an open question rather than answering it.
9. **In-memory storage**, not SQLite, given the stated volume (a few thousand/day, ~50 campaigns) and the time box. This trades away restart durability for implementation simplicity; revisited under Part 4.
10. **Concurrency**: a single global mutex-protected dedup table (atomic check-and-reserve on `event_id` in one critical section) plus per-campaign locks for counters; `GET stats` returns a snapshot copied under one lock hold, never a field-by-field read. At most one lock domain is ever held at a time, by construction, so there's no lock-ordering cycle to reason about.
11. **`GET /campaigns/{id}/stats` 404s for a `campaign_id` that has never had an accepted event.** There's no campaign-registration endpoint, so "unknown" and "no activity yet" are indistinguishable; ingestion stays permissive (any campaign_id creates state on its first accepted event) while the read path reports absence of state as 404.

### Ambiguities the brief leaves open (not resolved by inventing a requirement)

- **"How many opened," precisely** — the brief poses this explicitly as a question. We didn't guess; we expose both interpretations.
- **Whether `contact_id` is truly required** — inferred from an absence (the metadata-optionality callout), not a direct statement. A reasonable reader could argue it should be optional like `metadata` and simply excluded from `unique_opens` when missing; we chose the stricter reading and documented why.
- **What HTTP status should represent a duplicate / invalid / mixed-outcome batch** — the brief asks the question without an answer; we chose uniform 200 + per-item detail as one defensible answer among several.
- **Whether an unregistered campaign_id should 404 or return zeros** on `GET stats` — no registry exists either way, so this was ours to decide.
- **Retention** — nothing is said about how long dedup state or event data should be kept. We keep everything in memory for the life of the process; no eviction, no TTL.

### Questions I would ask the PM

- For "how many opened," do marketers actually look at both raw opens and unique opens, or only one — and if only one, which?
- When a batch has some invalid events, do you want that visible to the provider (so their integration owner notices), or just logged internally for us to chase?
- Is `contact_id` ever legitimately absent (e.g., anonymous or non-addressable sends), or is it safe to always require it?
- Does the dashboard need day-bucketed history (e.g. delivered-per-day, like the debugging exercise computes) or only current running totals?
- Any compliance/retention constraint on holding `contact_id` and `metadata` indefinitely in memory?

### Priorities for the time box, and what was intentionally skipped

Prioritized, in order: correctness of dedup/conflict handling → partial batch acceptance → concurrency correctness (this is also exactly what Part 3's bugs test) → `opened`/`unique_opens` semantics → stats endpoint. These are the decisions the brief explicitly flags as evaluated.

Intentionally skipped for Part 2, and why:
- **`GET /campaigns/{id}/events`** (paginated recent activity) — explicitly optional in the brief, lowest marginal value given the time box.
- **SQLite / any durable store** — explicitly "your choice"; in-memory chosen and argued above, SQLite named as the obvious next step in Part 4 rather than built now.
- **Causal/funnel validation** (e.g. rejecting an `opened` with no prior `delivered`) — would contradict the brief's own stated fact that out-of-order arrival is normal, so deliberately not built.
- **Auth, rate limiting, structured logging/metrics/tracing, graceful shutdown, config files, Docker** — explicitly out of scope per the brief ("do not set up deployment... authentication... Docker is optional and unnecessary").
- **A generic swappable storage interface** — no second backend is actually being built, so an abstraction for one would be premature.

---

## Part 4 — Scale memo (3k/day → 100M/day)

This is a design discussion, not something we built. Nothing below was implemented — the running service stays in-memory, single-process, no queue.

### What breaks first, specifically

1. **The single global dedup mutex (`Store.dedupMu`).** Every accepted/duplicate/conflict decision for *every* campaign serializes through one `sync.Mutex` guarding one map. At 100M/day (~1,157 events/sec average, but marketing traffic is bursty — a single 5M-contact send blasted over 10 minutes alone is ~8,000+ events/sec) this single lock becomes the throughput ceiling long before CPU, network, or the per-campaign locks do. It's also process-local, so it can't be scaled by adding more instances — two instances behind a load balancer would each have their own independent dedup table, and the same provider retry hitting two different instances before either durably records it would double-count. This is the first thing that actually breaks, not a secondary concern.
2. **Unbounded in-memory dedup growth.** `seenEvents` never evicts. At 100M events/day, even a modest per-entry footprint means tens of GB added per day with nothing ever freed — this OOMs the process within days, independent of the mutex problem.
3. **No durability.** All state — dedup table and per-campaign counters alike — lives in one process's RAM. A crash or restart loses everything, which is unacceptable once a large paying client's numbers are at stake (today it's an explicitly accepted tradeoff for a few-thousand/day internal tool; at 100M/day it stops being defensible).
4. **Single point of failure / no horizontal scaling.** The whole design assumes one process owns all state. There's no way to add capacity by running more instances without first solving where the shared state lives.

The per-campaign counter locks (`campaignState.mu`) and the campaign-map lock (`campaignsMu`) are *not* what breaks first — those scale fine algorithmically (O(1) map access, contention only within one campaign's own counters) as campaign count grows into the hundreds. The dedup table's single global lock and the total lack of durability are the real bottlenecks.

### Throughput / concurrency implications

100M/day is ~1,157 events/sec sustained, but real send traffic is bursty — a single large campaign send can itself exceed that for a period. The design needs to absorb bursts, not just sustain an average. This pushes toward: sharding the dedup check across many partitions (e.g. consistent-hash `event_id` into N shards, each independently lockable) instead of one map/mutex, and decoupling "accept the request quickly" from "durably record and aggregate," since doing both synchronously per HTTP request doesn't hold up under bursty load at this volume.

### Would the ingestion API change?

The wire contract to providers (the JSON event shape) shouldn't need to change — that's the fixed part of the deal with them. But the *processing model* behind `POST /events` likely would: today, validate → dedup → apply all happen synchronously within the request, and the response tells the caller exactly what happened to each item. At 100M/day, doing durable cross-shard dedup and aggregation synchronously per request risks blowing request latency and holding connections open under burst load. More likely: `POST /events` becomes a fast validate-and-durably-enqueue step (return a quick 2xx once the batch is safely on a queue/log), with the actual dedup/counting happening asynchronously downstream. That changes what the response means — "202, we have it" rather than "200, here's exactly how each item was classified" — a real, deliberate contract change I'd flag to the PM, not something to slip in silently.

### Durable storage and indexing

- **Dedup** needs to move from an in-process map to something that enforces uniqueness durably and across machines — e.g. a table with a unique constraint on `event_id`, or a distributed KV/wide-column store keyed by `event_id` with conditional-write ("insert if not exists") semantics. This replaces the correctness-critical role our `reserve()` critical section plays today, at a layer that can actually scale horizontally.
- **Aggregate counters**: naive live-row increments would create hot-row contention for popular campaigns (every event for one campaign serializing through one row). Prefer either sharded counters (several counter rows per campaign, summed at read time) or, better, storing the deduplicated raw events (partitioned by `campaign_id` + day) and computing aggregates via a separate batch/stream job. The second option is more auditable and directly supports the kind of day-bucketed breakdown the debugging exercise computes, without re-deriving business logic ad hoc.
- **Indexing/key strategy**: partition by `campaign_id` (matches the read pattern — `GET stats` is always scoped to one campaign) with `event_id` uniqueness enforced within/across shards; `unique_opens` needs its own `(campaign_id, contact_id)` index or set, analogous to today's `openedContacts`, potentially backed by an approximate structure (HyperLogLog) only if exact per-campaign contact sets are shown to be a real memory problem — not by default (see "what I would not solve," below).

### Would a queue help, and what does it cost?

Yes — a durable log/queue between ingestion and aggregation is the natural way to absorb bursts without forcing synchronous backpressure onto providers, and to decouple "durably received" from "counted." It also enables replay: if an aggregation bug is found later (exactly the kind of bug Part 3 is about), you can reprocess history instead of losing it.

What it costs:
- **New failure modes.** Consumer lag, and at-least-once delivery means the aggregator itself must be idempotent — the dedup problem doesn't disappear, it moves downstream and has to be solved there too (same event_id-keyed conditional write).
- **A second source of duplicates**: a queue redelivering a message after a consumer crashes before committing its offset, independent of provider-level retries. There are now two independent layers that can each produce a duplicate of the same logical event.
- **Operational cost**: another system to run, monitor, and capacity-plan — not free, and not something I'd introduce speculatively without the volume to justify it.

### Where duplicates newly sneak in

- Queue redelivery after a consumer crash/restart before its offset is committed.
- Multiple ingestion instances racing to accept the same provider retry before either durably records it — the in-process mutex that prevents this "for free" today doesn't help once ingestion is horizontally scaled; the storage layer's own uniqueness constraint has to be the real source of truth.
- Replay/reprocessing after a bug fix, if it's not itself dedup-safe against data already materialized (a replay should rebuild aggregates from raw deduplicated events, not reapply deltas on top of existing ones).
- Any internal retry a downstream stage makes against its own next stage — same problem, now happening inside our systems instead of at the provider boundary.

### Keeping counters trustworthy

The property that has to survive retries, concurrency, crashes, and replay is the same one our current `reserve()` encodes today, just moved to a layer with real cross-machine durability: **the dedup check-and-record must be atomic and durable before any counter is touched** — a storage-level unique constraint or conditional write, not a wall-clock mutex. Beyond that: prefer aggregates computed as idempotent functions over a durable, immutable log of deduplicated events (rebuildable, auditable) over directly-mutated running counters with no source of truth behind them. Run a periodic reconciliation job that recomputes aggregates from the raw log and diffs against what's being served — catching drift before a marketer does. The realistic target is "exactly-once effect," achieved through idempotent writes keyed by `event_id` at every stage — not "exactly-once delivery," which distributed systems generally can't guarantee and shouldn't be relied on for correctness.

### What I would monitor

- **Ingestion**: request rate, error rate, latency percentiles, queue lag (once introduced), rejection rate (a spike suggests a provider integration broke), conflict rate (a spike suggests a provider is misusing `event_id`).
- **Aggregation correctness**: the reconciliation job's diff size (served aggregate vs. recomputed-from-raw) as a first-class trustworthiness metric — alert on anything beyond a small expected lag window.
- **Dedup-store health**: observed duplicate rate (a sudden drop or spike is itself a signal), write latency/throughput.
- **Per-campaign growth rate vs. expected send volume** — the kind of sanity check that would have flagged the Part 5 scenario before it became a support ticket.
- Standard system health for whatever services and infrastructure get introduced (queue consumer lag, storage replication lag, service CPU/memory).

### What I would deliberately not solve yet

- **Strong real-time dashboard consistency.** Some bounded staleness (seconds) between ingestion and a stat reflecting it is an acceptable, deliberate tradeoff against the complexity of a fully synchronous pipeline at this volume.
- **Exactly-once delivery at the transport layer** — not generally solvable; solve idempotency at the effect layer instead, as above.
- **Multi-region active-active writes** — assume a single-region primary is fine until there's evidence otherwise; don't build cross-region conflict resolution speculatively.
- **Approximate cardinality structures for `unique_opens`** (e.g. HyperLogLog) — only if exact per-campaign contact sets are shown to be a real memory problem in practice. State here is proportional to distinct contacts, not event volume, so this may never actually be needed; don't trade correctness for a problem that hasn't been measured.
- **A generic, provider-agnostic ingestion plugin architecture** — no second provider integration is on the table; not worth building for one.

---

## Part 5 — The angry marketer

### Plausible explanations, before assuming the code is broken

1. **Late-arriving `delivered` events (most likely, and not a bug).** The brief states events arrive late and providers' own delivery pipelines take time to generate receipts. `sent` staying flat at 1,000,000 across both reads while `delivered` climbs is exactly the shape you'd expect 30 minutes after a large send finishes — the send is done, receipts are still trickling in.

2. **`opened` dropping is the actually surprising direction**, since our own design treats accepted counts as strictly additive — nothing in the ingestion/aggregation logic we built ever decrements a counter once an event is accepted. A real decrease in a supposedly monotonic number points toward the *read* or *deployment* layer rather than the counting logic itself:
   - **A service restart between the two reads.** Our own architecture holds all state in memory (a documented, deliberate Part 2 tradeoff for the current scale) — if the service restarted or redeployed between 10:00 and 10:30, in-memory counters reset and only rebuild from whatever gets reingested afterward. This could plausibly show `opened` lower at 10:30 while `sent`/`delivered` (fed by a different, perhaps already-larger backlog) look unaffected. This is a very concrete failure mode given exactly how we built things — worth checking first, not last.
   - **Reads hitting an inconsistent cache or lagging replica.** If the dashboard doesn't read a single consistent source of truth, two reads 30 minutes apart could reflect two different, non-monotonic snapshots even though the underlying data never actually shrank. This is exactly why Part 2 required `GET stats` to return one lock-protected snapshot rather than field-by-field reads — a marketer comparing two dashboard loads is implicitly trusting that both reflect the same growing ground truth.
   - **Bot / prefetch-open filtering changing mid-flight.** Email opens are frequently triggered by security-gateway or mail-client image prefetching, not a human. If a bot-classification rule was added or changed between the two reads (a batch job retroactively reclassifying some opens as non-human), a real, legitimate `opened` number could genuinely decrease. This is a business-logic phenomenon specific to email analytics, not a data bug.
   - **A metric-definition change mid-flight** (e.g. a deploy switching what "opened" means, raw vs. unique) landing between the two reads — unlikely in a 30-minute window, but not impossible.
   - **An actual bug** — an aggregation race losing increments (exactly the class of bug fixed in Part 3), or a crashed/partial recompute — stays on the list, just not jumped to first.

### What I'd check first, and why

1. **Re-query the same stats endpoint twice, back to back, right now.** If two near-simultaneous reads already disagree, that's a consistency/aggregation bug, not a business explanation — cheapest, fastest signal.
2. **Check whether the service restarted or redeployed between 10:00 and 10:30** (process uptime, deploy log). Directly tests the in-memory-reset explanation, which is the most architecture-specific and easiest to confirm or rule out.
3. **Check the dashboard's read path** — is it reading one consistent source, or a cache/replica that can regress? Tests the inconsistent-read explanation.
4. **Check whether a bot-filtering/classification rule changed** in the relevant window (deploy history for the analytics pipeline, not just ingestion).
5. **Only after ruling out the above**, look for a genuine aggregation defect — re-run a from-raw-events recompute (the same kind of reconciliation job recommended in Part 4) and diff it against what the dashboard showed at each timestamp.

### Is the delivered increase (970k → 975k) itself suspicious?

No — on its own, this is the expected, healthy shape of the funnel. A 1M-message send doesn't get 100% of its delivery receipts back instantly; `delivered` creeping upward for a while after `sent` holds steady is exactly what late-arriving receipts predict, not a red flag. It *would* warrant a look if `delivered` ever exceeded `sent`, or jumped by an amount disproportionate to any plausible provider processing delay — neither is true here.

### Data/measurement issues vs. real campaign behavior

Real campaign behavior explains monotonic increases consistent with normal delayed processing — the `delivered` rise is this. A data/measurement issue is anything that makes a counter that should only ever grow (raw, deduplicated, append-only, as ours is designed) appear to shrink between two reads — since nothing in our own ingestion/aggregation logic retroactively un-counts an already-accepted event, an *observed* decrease is a strong prior toward the read/deployment/definition layer, not the counting logic itself, and that's where I'd look first.

**Ties back to:** timestamp semantics (delayed `delivered` receipts are exactly the "events arrive late" fact from the brief, not an anomaly); provider retries/duplicates (ruled out here since our dedup makes duplicates a non-event for counting, not a source of apparent decreases); aggregation (a race or partial recompute is on the suspect list, just not first); and dashboard freshness/consistency (the most likely non-bug explanation given our own architecture is exactly a consistency or restart issue in the read path, not the count itself).
