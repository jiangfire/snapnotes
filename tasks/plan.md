# Implementation Plan: SnapNotes

Status: Phase 1–2 implemented and verified on 2026-08-20; Task 3.2 implemented and verified on 2026-08-20; Task 3.3 implemented and verified on 2026-08-20; Phase 4 implemented and verified on 2026-08-20; Phase 5 protocol amendment approved 2026-08-21; Task 5.1 (peak-bagging MMR, inclusion-proof gen/verify, byte-exact vectors, server+client migration) implemented and verified on 2026-08-22; Task 5.2 (headers-first sync, chainwork selection, reorganisation, malformed-peer rejection, LastChainwork persistence) implemented and verified on 2026-08-22; legacy-defect remediation round (M1/M2 JSON unknown-field rejection on trust boundaries, N1 dead-test fixed; C2 assessed as intentional design deviation) completed on 2026-08-22; TUI wiring round (Sync switched from legacy CatchUp to CatchUpHeadersFirst, regression test added) completed on 2026-08-22; CBOR block wire round (/blocks now returns a single canonical CBOR block decoded by protocol.DecodeBlock on the client, closing the M1 CBOR receive boundary) completed on 2026-08-22; /headers CBOR evaluation (assessed on 2026-08-22 — decided NOT to migrate /headers to CBOR; rationale recorded in the decision log below); Task 5.3 (MMR inclusion-proof serving + multi-node cross-device verification) implemented and verified on 2026-08-22.

## Objective

Build a Go/Bubble Tea local-first append-only notes TUI. Phase 1 delivers useful offline notes, parsing, SQLite-backed search, and a stable input flow. Later phases add client-only reminders, encrypted single-node synchronization, authorised multi-device writes, and finally optional multi-node verification.

## Commands and project conventions

| Purpose | Command |
| --- | --- |
| Focused Go package test | `$env:GOTELEMETRY='off'; go test ./internal/<package>` |
| Full test suite | `$env:GOTELEMETRY='off'; go test ./...` |
| Static analysis | `$env:GOTELEMETRY='off'; go vet ./...` |
| Format changed Go files | `gofmt -w <changed-files>` |

`GOTELEMETRY=off` is required in this environment because the Go telemetry directory is unavailable. Exact package-level commands are established when the first package and test are added.

## Architecture and dependency order

```text
Protocol v1 approval ────────────────> encrypted sync and membership (Phases 3–4)
        │
Local domain model ─> SQLite repository ─> TUI create/list ─> search
        │                                      │
        └──────────────────────────────────────> reminders
                                                    │
block/MMR exact format ────────────────────────────> multi-node verification (Phase 5)
```

The initial implementation uses small packages with one direction of dependency: `parser` and domain types remain pure; SQLite implements a repository boundary; the TUI orchestrates domain services but does not construct SQL. Network code must not enter Phase 1.

## TDD policy

For every behaviour, work in this order: a focused failing test (RED), minimal production code (GREEN), then a test-backed cleanup. Tests assert user-observable state, not call order. New pure logic gets small unit tests; SQLite and TUI boundary checks are medium integration tests; only critical interactive flows get end-to-end coverage.

## Phases and checkpoints

### Phase 0: project test baseline

Establish the test/package layout and confirm the repository's commands. No product feature is included.

### Phase 1: local notes

Deliver a vertical slice: create a note without network access, persist it, restart, and show it in the recent list. Add parser and local FTS search in subsequent small slices.

Checkpoint (passed 2026-08-20): `go test ./...`, `go vet ./...`, and `go build ./cmd/snapnotes` pass; persistence, Enter, multiline mode, parser, FTS search, and bounded pagination are covered by automated tests.

### Phase 2: local reminders

Add deterministic scheduling and local-only state. Time must be injected into domain tests; no tests may depend on wall-clock timing.

Checkpoint (passed 2026-08-20): deterministic reminder scheduling, UTC/timezone restoration across SQLite reopen, due/overdue/dismissed display, Ctrl+R acknowledgement, and daily-five selection are covered by tests.

### Phase 3: encrypted single-node sync

Starts only after `docs/protocol-v1.md` is approved and its v1 transaction vectors are committed. Implement client/server boundaries as encrypted transaction transport from the first slice; no production plaintext-sync stage.

Task 3.1 checkpoint (passed 2026-08-20): deterministic CBOR transaction vectors, transaction ID/signature/PoW/transaction-hash vectors, strict unknown/duplicate/trailing-field rejection, and X25519/HKDF/XChaCha20-Poly1305 envelope round-trip and tamper tests pass. No network endpoint was added before the protocol vectors.

Phase 3 checkpoint (passed 2026-08-20): Task 3.2 (offline outbox persistence, idempotent submission, server rejection of invalid auth/signature/size/stale-epoch with documented error codes) and Task 3.3 (anchor-aware catch-up from a saved `sync_state`, bounded `/headers`(≤2000)/`/blocks`(≤100) page sequence with `known_block_hash`→409 CHAIN_MISMATCH, notification-only WebSocket whose absence never loses changes) are implemented and covered by tests.

### Phase 4: membership and multi-device keys

The approved recipient-envelope and `key_rotation_bundle` contract govern this phase. Implement join, grants, revocation, and key rotation as protocol-level integration tests before TUI wiring.

Checkpoint (passed 2026-08-20): Task 4.1 (out-of-band anchor + signed join request verification, KeyStore per-epoch stream key recovery, tampered envelope/AAD/nonce/recipient-key rejection) and Task 4.2 (atomic `key_rotation_bundle` with no interleaving write window, revoked signing key cannot append, active devices receive new epoch, revoked device retains only previously held historical keys) are implemented and covered by 6 tests across `internal/protocol/membership_test.go` and `internal/api/server_test.go`. Full `go test ./...`, `go vet ./...`, and `go build ./...` pass.

### Phase 5: multi-node verification

Starts only after block-header and MMR proof wire formats, plus interoperability vectors, are approved. The protocol amendment was approved on 2026-08-21. It is intentionally not a prerequisite for a trusted single-node release.

Task 5.1 checkpoint (passed 2026-08-22): the peak-bagging MMR (`Node`/`MMRRootFromPeaks`/`AddLeaf`) and inclusion-proof generate/verify are implemented with byte-exact pinned vectors for 1/7/100 leaves and full tamper coverage; server write path and client verify path are migrated off the old hash-chain MMR onto the bagged MMR; Phase 3.3 catch-up tests still pass. `go test ./...`, `go vet ./...`, and `go build ./...` pass.

Task 5.2 checkpoint (passed 2026-08-22): `ChainManager` selects the active chain by cumulative chainwork (never height) and reorganises with MMR-state restore + orphan labelling on a strictly-greater-work fork; malformed/tampered headers and chains not sharing the trust-anchor genesis never advance local state; the headers-first `CatchUpHeadersFirst` path persists the verified active tip (including a 32-byte cumulative `LastChainwork`) to `sqlite.SyncState` and finalises only the active chain's blocks. Covered by `internal/protocol/chainwork_test.go`, `internal/sync/chain_test.go` (vectors 4/5/6 + tamper/MMR/out-of-order guards), and `internal/sync/catchup_headers_test.go` (end-to-end headers-first + mismatched-genesis rejection). `go test ./...`, `go vet ./...`, and `go build ./...` pass. Outstanding non-blocking items carried from earlier phases (C2 envelope AAD zero-value transaction_id; M1/M2 strict wire decode on receive; N1 dead assertion) remain for a unified later cleanup.

TUI wiring checkpoint (passed 2026-08-22): the public `Sync` entry point now performs the headers-first catch-up — `Sync` calls `CatchUpHeadersFirst` instead of the legacy `CatchUp`, so the live TUI path gains chainwork-based chain selection, reorganisation, and malformed-peer rejection. `CatchUp` is retained for direct testing. `Listen`'s doc comment was updated to reference `Sync` (which routes through headers-first). Locked in by `internal/sync/catchup_headers_test.go::TestSyncRoutesThroughHeadersFirst`, which asserts `Sync` persists a non-zero `LastChainwork` (a property only `CatchUpHeadersFirst` produces) and is idempotent. `go test ./internal/sync/ ./internal/api/` pass.

CBOR block wire checkpoint (passed 2026-08-22): `/blocks` now returns each block as a single canonical CBOR item (`protocol.MarshalBlock`: header + block_hash + transaction) base64url-encoded in a `block` field, instead of a JSON envelope. The client decodes it with `protocol.DecodeBlock` (via `decodeCborBlock` in both the headers-first `fetchBlock` and legacy `CatchUp`), which strictly rejects non-canonical CBOR, unknown fields, and trailing data — closing the M1 CBOR receive boundary. The server adds `marshalBlock` (re-wrapping stored transaction CBOR via the new `notesync.DecodeSignedTransaction`) and drops the old `txToWireJSON` JSON path; a `CodeInternal` error code was added for marshal failures. Locked in by `internal/sync/chain_test.go::TestDecodeCborBlockRejectsMalformed` (unknown-field and trailing-data CBOR blocks are rejected; a well-formed block decodes). Full `go test ./...`, `go vet ./...`, `go build ./...` pass. Note: `/headers` remains a lightweight JSON header envelope (chain selection needs only the header); `DecodeBlock`/`MarshalBlock` are no longer dead code.

Task 5.3 checkpoint (passed 2026-08-22): the MMR inclusion-proof primitive is now wired end to end, closing the "multi-node verification" item in the Phase 5 title. The server accumulates an ordered `leafHashes` set on every accepted block (genesis + one leaf per block) and serves `GET /api/v1/streams/{id}/proof?leaf_index=N` — it recomputes the proof from the full leaf set with `protocol.GenerateInclusionProof` and returns the canonical-CBOR proof (`MarshalMMRProof`) base64url-encoded. The client adds `SyncClient.VerifyLeafInclusion(ctx, leafIndex)`: it GETs the proof, strictly decodes it (`DecodeMMRProof`), and verifies it against the device's own locally-pinned active-tip `mmr_root` (`state.LastMMRRoot`, never a server-asserted value) with `protocol.VerifyInclusionProof`, using `totalLeaves = LastBlockHeight + 1`. This is the core multi-node verification capability: a client independently proves a historical transaction belongs to the chain it trusts without re-downloading the whole MMR. Locked in by `internal/sync/catchup_headers_test.go::TestCrossNodeVerifyLeafInclusion` — an independent node B syncs from node A, verifies leaves 0/2/N as true, proves a tampered leaf hash fails, and confirms an out-of-range index and a no-sync state are rejected. Two latent bugs were also fixed during wiring: (1) `ChainManager.FinalizeBlock` now derives the MMR peaks from the already-finalized parent header instead of the stale header-phase snapshot, so the client's cumulative mmr_root actually matches the server's; (2) `CatchUpHeadersFirst` finalizes before persisting (and always re-derives peaks from genesis on each rebuilt ChainManager) so a re-sync resume does not skip blocks and mis-verify. Full `go test ./...`, `go vet ./...`, `go build ./...` pass.

## Risks and mitigations

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Protocol ambiguity creates incompatible clients | High | Approve `docs/protocol-v1.md`; commit byte-level vectors before network code |
| Crypto misuse | High | Fixed primitives/envelope, test vectors, threat-model tests, no custom primitives |
| Local-first UX becomes blocked by network | High | Keep create/persist/outbox in one local transaction; network runs separately |
| Scope expands into multi-node consensus too early | High | Keep Phases 1–4 single-node; Phase 5 has a hard protocol gate |
| Time-dependent reminder tests flake | Medium | Inject clock and timezone into scheduling functions |

## Approval gates

The Phase 3–4 protocol gate is satisfied: the atomic `key_rotation_bundle` operation and strict current-epoch-only PoW acceptance rule were approved on 2026-08-20. Phase 5's exact block-header and MMR-proof amendment was approved on 2026-08-21 and Task 5.1 is implemented and verified as of 2026-08-22; Phase 5.2 (headers-first sync, chainwork, reorganisation, malformed-peer rejection) remains.

## Decision log

### 2026-08-22 — Do NOT migrate GET /headers to CBOR (keep JSON)

**Context.** After the CBOR block wire round made `/blocks` return a single canonical
CBOR block (`protocol.MarshalBlock` on the server, `protocol.DecodeBlock` on the
client, closing the M1 CBOR receive boundary), the question was raised whether
`/headers` should also be migrated to CBOR for protocol uniformity.

**Decision.** Leave `/headers` as JSON. Do not add a CBOR header codec.

**Rationale (code-fact based).**
- The M1 goal — close the client *receive* trust boundary — is already fully met:
  the three CBOR trust boundaries (server submit acceptance, local outbox, client
  block receive) all decode strictly. `/headers` already parses with
  `json.Decoder.DisallowUnknownFields()`, an equal strictness for its scalar +
  two-hash content.
- `/headers` carries only scalar fields and two base64 hashes; there is no
  composite signed/AAD structure (the transaction carries that), so the
  "JSON silently drops a field" risk that motivated `/blocks` CBOR does not apply.
- Cryptographic binding of a header comes from the server-recomputed `block_hash`
  and `ChainManager.ApplyHeader` (prev_hash continuity, genesis match) — unchanged
  by the wire encoding.
- Cost of migrating: new `protocol.MarshalHeader`/`DecodeHeader` (canonical +
  strict), rewrite of `headerEntry`/`toWireHeader` (server) and
  `headerPage`/`headerEntryLocal`/`decodeWireHeader`/`wireBlockHeader` (client),
  rework of `TestCatchUpHeadersFirstRejectsUnknownHeaderField` (inject malformed
  CBOR instead of JSON), and a breaking wire change — for no security gain.

**Trigger to revisit.** Migrate `/headers` to CBOR only if (a) the whole sync wire
protocol is intentionally unified on CBOR for future binary/bandwidth optimisation,
or (b) headers start carrying composite structures (e.g. transaction digests) where
JSON silent field-drop would become a real risk.
