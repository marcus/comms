# Overnight coordination feedback

Implementation completed for the eight verified Comms tickets; publish as additive v1.5.0. Independent Sol
worktrees own implementation groups; one integration worktree resolves shared
CLI, application, registry, and store edits. Keep the existing checkout's
untracked diagrams and all live Comms identities, topics, and inboxes untouched.

## Groups and behavior

- `td-5a87d0`, `td-80c0a8`: prefer stable Codex conversation identity before
  tmux fallback; retain explicit overrides and takeover refusal. Rejoining the
  same handle from its existing session succeeds without hijacking another
  session. Document implicit identity migration.
- `td-23e485`, `td-de1d19`: acknowledge all followed topics, optionally bounded
  by a returned cursor, through the serialized writer. Preserve monotonic read
  positions and wait/read cursor independence. Keep publishing's follow
  requirement and name the recovery command in the refusal.
- `td-6ac320`: optional message kind and server-side inbox/wait kind filters.
  Use migration 003/schema 3; retain optional reply titles and compatible
  existing message shapes. Explain coordination conventions.
- `td-453d73`, `td-cea8f7`: searchable, recent-first agent discovery and close
  handle suggestions; expose advisory inbox/wait timestamps and open waits.
  Activity hints describe the daemon's observations, never delivery guarantees.
- `td-1c78dd`: independently verify existing inbox previews, `--full`, full wait
  responses, read independence, receipt contract, and Comms Web compatibility.

## Integration and acceptance

1. Integrate scoped commits sequentially and resolve overlapping adapters.
2. Verify each group with focused tests and real CLI commands against temporary
   stores and fake identities. Run Comms Web contract checks against its current
   consumer; modify it only if the integrated contract requires it.
3. Run `make check` and `git diff --check` on the integrated candidate. Obtain
   independent Sol review of every ticket and fix concrete findings.
4. Record the implemented behavior in README, registry instructions/OpenAPI,
   and the changelog. Move this plan to implemented when acceptance is green.
5. Fast-forward main without disturbing unrelated files. Use the established
   `make release` workflow for v1.5.0 and verify remote main/CI, annotated tag,
   four platform archives and checksums, and the published Homebrew formula.
6. Verify the installed v1.5.0 binary and Homebrew test, then repeat a clean-store
   conversation. Close td tickets only with actual independent review evidence.

## Evidence

Baseline: `eb0e869` / v1.4.0 on `marcus/comms` main, aerie. All eight ticket
records resolve. The existing `comms-web` sibling is the web consumer; there is
no separate comms-ui checkout. Implementation evidence is recorded below; publication and installed-consumer evidence are tracked in `td-71b65f`.

Verified implementation candidate: `d395344`. `make check` passed, including the
complete race suite, vet, and lint with zero issues; `git diff --check` passed.
Independent Sol review `review-comms-overnight` / `ses_4fb604` is clean for every
story and the retired-reader fix `td-b64ab9`. Real temporary-store CLI smoke
verified kind filters, full bodies, wait continuation, bounded/all acknowledgement,
activity cleanup, discovery, suggestions, follow recovery, and optional titles.
Comms Web receipt tests passed 7/7 and its existing contract remains compatible.
