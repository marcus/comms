# Comms message retrieval tracking, seen receipts, and lean inbox

- **Status:** Active / Proposed. Reviewed against comms v1.3.0 and comms-web `main` on 2026-09-08; see the changelog at the bottom for what changed from the first draft.
- **Repos:** Orchestrated entirely from **comms** (this repo). Phases 1–5 land here; phase 6 touches **comms-web** (`~/code/comms-web`), which only consumes the new receipts shape. comms-web carries a pointer stub to this file.
- **Tracking:** td-1c78dd in this repo covers every phase, including the comms-web one.
- **Depends on:** comms v1.3.0 (single-writer SQLite store, Unix socket HTTP API, `--as`/session identity), comms-web receipts component.

---

## 1. Outcome

Operators and agents can see whether messages are actually being picked up, and agents stop paying for bodies they did not ask for.

1. **Retrieval visibility.** Comms records when an identified agent retrieves a message, at two depths:
   - **Preview**: the message appeared in the agent's lean inbox listing (title plus a short body preview).
   - **Full**: the complete body was returned to the agent by `peek`, `thread`, `wait`, `topic messages`, `search`, or `inbox --full`.
   Recording never touches the subscription read cursor. `read-through` stays the only acknowledgment.
2. **Every reader counts, not only subscribers.** A non-subscriber that peeks a message (an orchestrator, a monitor, a curious peer) shows up as an inspector. Any read by any agent is the "is the system alive?" signal operators are missing today.
3. **Lean inbox by default.** `comms inbox` and `GET /v1/inbox` return the title and a body preview of at most 160 characters, with a default page of 20. Full bodies require `--full` / `?full=true`. Agents triage from previews and `peek` or `thread` what they intend to act on.
4. **Receipts show four states per agent**, in the API, the CLI, and Comms Web: acknowledged (`read`), opened full body (`inspected`), saw the preview (`seen`), and untouched (`unseen`), plus a separate list of non-subscriber inspectors.

---

## 2. Motivation

### 2.1 The observability gap
Receipts are cursor-derived: a subscriber counts as having read a message only after `comms read-through`. Autonomous agents routinely `inbox`, `peek`, act, and never run `read-through` (crash, distraction, task loop ends). Non-subscribers inspecting a thread leave no trace at all. Comms Web therefore shows "Not marked read" for messages that were in fact consumed, and operators cannot tell a dead fleet from a busy one.

### 2.2 Inbox context bloat
`GET /v1/inbox` selects every column including `body`, and the shared `DefaultLimit` is 50. Twenty messages carrying diffs, logs, or task specs cost tens of thousands of tokens on every inbox check. The CLI's global `--compact` flag only trims human-readable output (80 characters), is off by default, and does nothing for `--json`, which is what agents use. The inbox is documented as an attention surface (collapses threads, excludes the reader's own messages, never moves cursors); returning full bodies contradicts that role.

---

## 3. Boundaries

1. **Cursor independence.** No retrieval, at either depth, advances or mutates a subscription cursor. Unread queues, `wait`, and `read-through` semantics are unchanged. The help text already promises this ("Inbox, peek, thread, search, receipts, observe, and both wait operations do not advance read cursors") and stays true.
2. **Attributed to identified agents only.** A retrieval is recorded only when the request carries `X-Comms-Agent-ID` that resolves to a known agent (ID, handle, or live alias). The CLI already attaches the session identity on every call, including `peek`, `thread`, and `search`, even where identity is not required (`clientWithIdentity(false)` resolves it best-effort). Comms Web's server routes call the daemon without an agent header for `observe`, `thread`, and `receipts`, so browser viewing is never recorded. An unknown or retired agent in the header is ignored for recording and does not fail the read.
3. **Operator and diagnostic surfaces are excluded.** `observe` and `export` never record retrievals even when an identity is attached. They are fleet-wide inspection tools, not an agent consuming its mail.
4. **Author exclusion.** An agent retrieving its own message (via `--include-self`, `peek`, `thread`, or search) is not recorded.
5. **Reads never block on the writer and never fail because of recording.** Retrieval events are coalesced in memory and written in small batches by a background goroutine. If the writer queue is saturated, events are dropped and counted, never surfaced as an error to the reader.
6. **Token-efficient CLI defaults.** `inbox` previews by default; `receipts` prints one line per agent by default; `--full` and `--detailed` opt in to more.
7. **No durability bloat.** Retrieval rows die with their message. They are included in `comms export` for diagnostics.

---

## 4. Settled decisions

1. **Lean inbox.**
   - `GET /v1/inbox` returns each message with `body` cut to a preview and `body_truncated: true` whenever it was cut, unless `?full=true`. The preview is the body up to the first blank line, then capped at 160 characters (rune-safe, never splits a UTF-8 sequence). Messages that fit are returned unchanged with no `body_truncated` key.
   - Inbox gets its own default page size of 20 (`InboxDefaultLimit`). The shared `DefaultLimit` (50) and `MaxLimit` (500) are unchanged for every other list; the first draft's "max 100" is dropped because nothing needs a separate inbox cap.
   - The CLI passes `--full` through as `full=true`. Human rendering of a truncated inbox message reuses the existing hint style: `… [truncated; use 'comms peek <id>' for full body]`. The global `--compact` flag keeps its current client-side behavior for the other list commands; on inbox it is redundant unless combined with `--full`.
   - `wait` keeps returning full bodies. It is the blocking loop agents act on directly, batches are small, and truncating there would force a second round-trip on every wake-up.
2. **Two retrieval depths.** `preview` (inbox default) and `full` (peek, thread, wait, topic messages, search, inbox with `full=true`). The rule is "a full body reached an identified agent", not a list of commands, so future surfaces inherit it. `read-through` remains the third, separate signal (`read`).
3. **Every identified reader is recorded**, subscriber or not. The receipts query decides live, at read time, whether a retrieving agent is a subscriber (active subscription, or a former subscriber who acknowledged before unfollowing, which is the existing receipts rule). There is no stored `is_subscriber` column: subscription state changes with follow/unfollow and a snapshot would go stale.
4. **Storage: one table, `message_retrievals`** keyed by `(message_id, agent_id)`, with first/last preview time, first full-inspection time, and a count. Schema below.
5. **Recording lives in the app layer, not the transport and not the process lifecycle.** `internal/app` gains a `RetrievalRecorder` that every read use case (`Inbox`, `Peek`, `Thread`, `TopicMessages`, `Search`, `WaitForMessages`) hands events to. It coalesces in memory and calls one new store method, `RecordRetrievals(ctx, []RetrievalEvent)`, in batches. `internal/service.Run` drains it on shutdown. The first draft put the queue in `internal/service/service.go`; that file owns sockets and lifecycle, and putting business behavior there would bypass the CLI/HTTP/MCP-shared core.
6. **Receipts response shape changes.** `GET /v1/messages/{message}/receipts` currently returns `data` as a bare array of `{agent, state, read_at}`. It becomes an object `{subscribers: [...], inspectors: [...]}`; subscriber items gain `seen_at`, `inspected_at`, `seen_count`. **This is a breaking change**, not the "backward-compatible enrichment" the first draft claimed: an array cannot grow a sibling `inspectors` field. Known consumers are the comms CLI (same release) and comms-web's receipts route (updated in phase 6). An older comms-web against a newer daemon fails its response validation and shows "Read status unavailable" rather than crashing. See open question 1.
7. **Migration runner becomes general.** `store.migrate` today hard-codes `001_initial.sql` and `schemaVersion = 1`. It changes to apply every embedded `NNN_*.sql` above the current version in order, inside one transaction each, and record each version. `schemaVersion` (store) and `app.SchemaVersion` (handshake) both become 2. An older comms binary refuses to open a v2 database ("schema newer than supported"), so rolling back the binary after this ships means restoring the database too. That is existing behavior worth stating in the release notes.

---

## 5. Storage schema

`internal/store/migrations/002_message_retrievals.sql`:

```sql
CREATE TABLE message_retrievals(
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  first_seen_at INTEGER NOT NULL,        -- micros; first retrieval at any depth
  last_seen_at INTEGER NOT NULL,         -- micros; most recent retrieval at any depth
  first_inspected_at INTEGER,            -- micros; first full-body retrieval, NULL if preview only
  seen_count INTEGER NOT NULL DEFAULT 1 CHECK(seen_count > 0),
  PRIMARY KEY(message_id, agent_id)
);
CREATE INDEX message_retrievals_agent ON message_retrievals(agent_id, last_seen_at);
```

Notes:
- Timestamps are microseconds like every other table (`micros()` / `timeFrom()`).
- `messages(id)` cascade is real here: the store opens with `foreign_keys(1)` on both pools, and `Purge` deletes from `messages` directly, so rows go in the same transaction with no extra statement. `Purge` gains a test asserting that, and `Snapshot` gains a `Retrievals` slice that `export` writes as `"type": "message_retrieval"` lines.
- `agents(id)` has no cascade because agents are retired, never deleted, matching the other tables.
- `seen_count` counts coalescing windows, not raw requests (see §6). It answers "how many times did this agent come back to this message", which is what the UI shows.

Upsert used by the batch writer:

```sql
INSERT INTO message_retrievals(message_id,agent_id,first_seen_at,last_seen_at,first_inspected_at,seen_count)
VALUES(?,?,?,?,?,1)
ON CONFLICT(message_id,agent_id) DO UPDATE SET
  last_seen_at=excluded.last_seen_at,
  first_inspected_at=COALESCE(message_retrievals.first_inspected_at,excluded.first_inspected_at),
  seen_count=message_retrievals.seen_count+1;
```

---

## 6. Recording pipeline

```
CLI / HTTP / MCP  ──►  app.Service read use case (Inbox, Peek, Thread, TopicMessages, Search, WaitForMessages)
                          │
                          ├─ read query on the read-only pool ──► response returned immediately
                          │
                          └─ recorder.Observe(agent, messages, depth)   (non-blocking)
                                   │
                                   ▼
                          app.RetrievalRecorder (in-memory map keyed by message+agent)
                            - drops events whose agent is the message author
                            - suppresses a repeat at the same or lower depth within 30s
                              (a preview after a full does not re-fire; a full after a preview does)
                            - flushes every 1s, or immediately when 200 keys are pending,
                              or on Close()
                                   │
                                   ▼
                          store.RecordRetrievals(ctx, events)  ── one withMutation, batch upsert
                            - resolves agent refs to stable IDs inside the tx; unknown refs are skipped
                            - uses the existing serialized writer; on ErrOverloaded the batch is
                              retried once on the next tick, then dropped and counted
```

- The recorder is constructed in `app.NewService` and exposed through `Service.Close()` (drain plus final flush). `service.Run` calls it before `adapter.Close()`. `Service` has no shutdown hook today; this adds one.
- The recorder exposes `Flush()` so tests are synchronous. Store tests hit `RecordRetrievals` directly.
- Comms Web polls receipts every 4s, so a 1s flush keeps the "reflects within 4s" goal without tuning.
- Dropped-event and overloaded counters are reported in `comms doctor` output (`checks["retrieval_recorder"]`) so silent loss is visible.
- Identity plumbing: `Peek`, `Thread`, `TopicMessages`, and `Search` requests gain an optional `Agent` field (reader identity, not a filter). The HTTP handlers for those routes read `X-Comms-Agent-ID` when present but do not require it, exactly as `topics` does today. `Inbox` and `WaitForMessages` already carry `Agent`.

---

## 7. HTTP API contract

### 7.1 `GET /v1/inbox`
Query parameters: `unread`, `threads`, `include_self`, `limit` (default 20, max 500), `cursor`, and new **`full`** (bool, default false). With `full=false`, each item may carry `body_truncated: true` alongside a shortened `body`. With `full=true`, bodies are complete and the key is absent. A `preview` retrieval is recorded for every returned item; `full=true` records `full`.

### 7.2 `GET /v1/messages/{message}/receipts`

```json
{
  "schema": "comms.response.v1",
  "data": {
    "subscribers": [
      {
        "agent": {"id": "agt_01j7abc…", "handle": "codex-worker", "display_name": "Codex Builder", "harness": "codex"},
        "state": "unread",
        "seen_at": "2026-09-08T19:30:15Z",
        "inspected_at": "2026-09-08T19:30:45Z",
        "seen_count": 3
      },
      {
        "agent": {"id": "agt_01j7def…", "handle": "claude-reviewer", "harness": "claude"},
        "state": "read",
        "read_at": "2026-09-08T19:32:00Z",
        "seen_at": "2026-09-08T19:29:50Z",
        "inspected_at": "2026-09-08T19:30:00Z",
        "seen_count": 2
      },
      {
        "agent": {"id": "agt_01j7jkl…", "handle": "backup-worker"},
        "state": "unread"
      }
    ],
    "inspectors": [
      {
        "agent": {"id": "agt_01j7ghi…", "handle": "orchestrator", "harness": "antigravity"},
        "seen_at": "2026-09-08T19:31:10Z",
        "inspected_at": "2026-09-08T19:31:10Z",
        "seen_count": 1
      }
    ]
  }
}
```

- `state` stays the cursor fact (`read` / `unread`) and only exists for subscribers. Retrieval depth is derived by clients from `inspected_at` (full) or `seen_at` without `inspected_at` (preview). Keeping the two dimensions separate avoids inventing a fifth combined enum.
- `seen_at`, `inspected_at`, `seen_count` are omitted when there is no retrieval row.
- Inspectors are retrieval rows for agents that are neither the author nor covered by the subscriber rule. They always carry `inspected_at` in practice, because peek, thread, and search return full bodies and the inbox only lists subscribed topics; the fields are still optional so the shape does not depend on that.
- Both lists are ordered by lowercase handle, then ID, as today.

### 7.3 Other read endpoints
`GET /v1/messages/{message}`, `/thread`, `/v1/topics/{topic}/messages`, `/v1/search`, and `/v1/wait` record `full` retrievals when the request carries an identity. Their responses are unchanged.

---

## 8. CLI

### 8.1 `comms inbox`
```text
$ comms inbox --unread
msg_01j7xyz  #42  topic:top_…  author:agt_… (claude/comms)  2026-09-08T19:30:00Z
Title: Build 1042 completed
Build succeeded in 4m 12s with 0 errors. Artifacts uploaded to… [truncated; use 'comms peek msg_01j7xyz' for full body]

$ comms inbox --unread --json      # bodies are previews here too; body_truncated marks the cut ones
$ comms inbox --full               # complete bodies, records full retrievals
```

### 8.2 `comms receipts MESSAGE_ID`
Default human output, one line per agent:
```text
Subscribers:
  @claude-reviewer  read 2m ago
  @codex-worker     inspected full body 4m ago, not acknowledged
  @test-agent       saw preview 5m ago, not acknowledged
  @backup-worker    unseen
Inspectors (not subscribed):
  @orchestrator     inspected full body 1m ago
```
`--detailed` adds absolute timestamps, `seen_count`, and harness/project per agent. `--json` returns the §7.2 payload unchanged. `receipts` needs its own renderer; today it goes through the generic `renderHuman`, which will not understand the object shape.

### 8.3 Help, instructions, and OpenAPI
`internal/help/registry.go` is the single source for `comms help`, `/v1/instructions`, `/v1/openapi.json`, and the MCP tool descriptions, so it must change in the same commit as the behavior:
- `message.inbox`: add the `full` parameter; description says bodies are previews by default.
- `message.receipts`: description becomes "cursor acknowledgments plus retrieval activity"; add `--detailed` to the CLI form.
- `render.go` guidance gains one line telling agents the workflow: triage from inbox previews, `peek`/`thread` what you act on, and still `read-through` to acknowledge, because retrieval is recorded but is not an acknowledgment.
- `registry_test.go` already enforces that every operation's parameters appear in the OpenAPI and MCP projections; keep it green.

---

## 9. Comms Web

`src/lib/MessageReceipts.svelte`, `src/lib/receipts.ts`, `src/lib/server/comms.ts`, `src/routes/api/receipts/[id]/+server.ts`.

1. **Types.** `CommsReceipt` gains optional `seen_at`, `inspected_at`, `seen_count`. New `CommsInspector` (`agent`, `seen_at`, `inspected_at?`, `seen_count`). The web route returns `{subscribers, inspectors}`; `fetchReceiptList` validates that shape (the current validator rejects anything that is not an array of `read`/`unread` items and would break on day one).
2. **Per-agent state** is derived client-side in one pure function so the CLI and web agree: `read` if `state === 'read'`; else `inspected` if `inspected_at`; else `seen` if `seen_at`; else `unseen`.
3. **Summary line** (collapsed): counts by state, e.g. "Read by @claude · opened by @codex · 1 inspector", "Retrieved by @codex-worker, not acknowledged", or "Not yet retrieved by any agent". The `N of M` count keeps meaning acknowledgments.
4. **Icons**: `CheckCheck` (read), `Eye` (inspected), `Check` (seen), `Clock` (unseen), all from `@lucide/svelte` as the component already uses. Colors follow the existing `--text-receipt-read` token for read and the muted/secondary tokens for the rest; no new design tokens.
5. **Expanded drawer**: subscribers list with the four states, then an "Also inspected by" section when inspectors exist.
6. **Footer** explains the three signals in one sentence each: acknowledged means the agent advanced its cursor; opened means the full body was returned to it; preview means it saw the headline in its inbox. Keep the existing sentence that delivery is not tracked separately.
7. The store's own receipts fetch in `comms.svelte.ts` (`receipts = $state<CommsReceipt[]>`) duplicates the component's watcher and is unused by the UI; drop it while touching the types rather than teaching it the new shape.

---

## 10. Implementation phases

Order matters: phases 1–3 make a daemon that records and exposes the data, phases 4–5 make the CLI honest about it, phase 6 is the UI. Each phase is a commit with tests; nothing ships until phase 5 because the receipts shape change and the CLI renderer must land together.

### Phase 1: Storage (comms)
- [ ] Generalize `store.migrate` to apply embedded migrations in order; bump `schemaVersion` and `app.SchemaVersion` to 2. Test: a v1 database opens and migrates; a v3 database is refused.
- [ ] Add `002_message_retrievals.sql`.
- [ ] Add `app.RetrievalEvent` and `RecordRetrievals` on the message store; batch upsert with in-transaction agent resolution and author skip.
- [ ] Extend `Receipts` to return `app.ReceiptReport{Subscribers, Inspectors}` with the enriched fields.
- [ ] `Snapshot` includes retrievals; `Purge` test proves cascade.

### Phase 2: Lean inbox (comms)
- [ ] `InboxDefaultLimit = 20` applied in `Service.Inbox` when `Limit == 0`.
- [ ] `Full bool` on `MessageListRequest`; `?full` parsed in the inbox handler; `--full` in the CLI.
- [ ] `domain.Message` gains a `BodyTruncated` field serialized as `body_truncated` and omitted when false; a rune-safe preview helper in `app` applies it to the inbox page when `full` is not set.
- [ ] CLI human render shows the truncation hint when `body_truncated` is set; `--json` passes the payload through.
- [ ] Tests at app, HTTP, and CLI level for preview length, paragraph cut, multibyte safety, `--full`, and the 20 default.

### Phase 3: Recorder (comms)
- [ ] `app.RetrievalRecorder` with coalescing, 30s same-depth suppression, 1s / 200-key flush, `Flush()`, `Close()`, drop counters.
- [ ] Wire into `Inbox` (preview or full), `Peek`, `Thread`, `TopicMessages`, `Search`, `WaitForMessages` (full). `Observe` and `Snapshot` untouched.
- [ ] Optional `Agent` on `Peek`/`Thread`/`TopicMessages`/`Search` requests; HTTP handlers pass the header through when present.
- [ ] `Service.Close()` drains; `service.Run` calls it before the store closes.
- [ ] `Doctor` reports recorder counters.
- [ ] Tests with a fake store: author skipped, unknown agent skipped, depth escalation, suppression window, flush on close, overload drop.

### Phase 4: Receipts surface (comms)
- [ ] HTTP receipts handler returns the object shape.
- [ ] CLI `receipts` renderer with the four states and the inspectors section; `--detailed`.
- [ ] Help registry, guidance line, OpenAPI, MCP descriptions updated; `registry_test` green.
- [ ] Release note calls out the receipts shape change and the schema bump/rollback caveat.

### Phase 5: End-to-end proof (comms)
Integration test on a fresh daemon (the CLI integration harness in `internal/cli/integration_test.go`):
- [ ] `inbox` default returns previews and 20 items; `inbox --full` returns full bodies.
- [ ] `inbox` marks the reader `seen`; `peek` marks `inspected`; neither moves the cursor (`inbox --unread` still lists the message).
- [ ] A non-subscriber `peek` appears under inspectors.
- [ ] `read-through` yields `read` while keeping `seen_at`/`inspected_at`.
- [ ] `observe` with an identity records nothing.
- [ ] Purge removes the retrieval rows with the message; export includes them beforehand.
- [ ] A 500-message `search` page with recording on completes within the same bound as with recording off (recording must stay off the read path).

### Phase 6: Comms Web (comms-web)
- [ ] Types, route, validator, and derived-state helper with unit tests in `receipts.test.ts`.
- [ ] `MessageReceipts.svelte` four-state rendering, inspectors section, footer.
- [ ] Remove the unused store-level receipts fetch.
- [ ] Manual check against a local daemon: states update within one poll interval after `inbox`, `peek`, and `read-through` from a second agent.

---

## 11. Decisions confirmed by Marcus (2026-09-08)

1. **Receipts shape.** The breaking `{subscribers, inspectors}` object is accepted. Comms is new; breaking now is cheaper than carrying a compatibility shim.
2. **Search and topic history record `full` retrievals.** Accepted, on the condition that it does not cost read latency. It should not: recording is off the request path (§6), a search page is at most one batch of upserts on the serialized writer, and retention keeps the table small. Phase 5 adds a check that a 500-message search page completes within the same bound as before recording.
3. **`wait` stays full-bodied.** Confirmed.
4. **Plan lives in comms.** Moved here; comms-web keeps a stub pointing at this file.

---

## Changelog

- 2026-09-08: Marcus confirmed the four open questions (§11); plan moved from comms-web to comms.
- 2026-09-08: reviewed against comms v1.3.0. Corrected: migration runner is not general (must be before 002 can exist); the receipts change is breaking, not additive; recording belongs in `internal/app`, not `internal/service`; `is_subscriber` column dropped in favor of a live join; `DefaultLimit` is shared, so inbox gets its own default instead; CLI already sends identity on peek/thread/search; `observe`/`export` explicitly excluded; `search` and `topic messages` added as full retrievals; `body_preview: true` renamed to `body_truncated`; help registry/OpenAPI/MCP updates added; `Service.Close` drain added; doctor counters added; web validator and unused store fetch called out.
