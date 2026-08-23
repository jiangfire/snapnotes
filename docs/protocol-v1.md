# SnapNotes Protocol v1

Status: accepted for Phases 3–4 on 2026-08-20; Phase 5 amendment approved on 2026-08-21; Task 5.1 (peak-bagging MMR, inclusion-proof gen/verify, byte-exact vectors, server+client migration) implemented and verified on 2026-08-22.

This document resolves the implementation-critical gaps in `project-spec.md`. It is deliberately narrow: it defines the contracts needed by independent clients and servers. Human-readable JSON is a transport representation only; hashes and signatures always use the canonical CBOR defined here.

## Normative conventions

- All hashes use SHA-256. A hash is exactly 32 bytes.
- All identifiers and public keys are byte strings, never text strings inside canonical CBOR.
- `stream_id`, `note_id`, `transaction_id`, and `block_hash` are 32-byte values. IDs are generated with a CSPRNG unless derived below.
- Integers are unsigned CBOR integers. Times in signed objects are UTC Unix milliseconds. Wire-level JSON times are RFC3339 strings.
- Every signed or hashed CBOR map uses only the fields listed in its schema. Unknown and duplicate fields are rejected. Optional fields are omitted rather than encoded as `null`.
- `canonical(x)` means RFC 8949 deterministic CBOR, including deterministic map-key ordering and shortest valid integer encoding.
- Literal domain separators below are ASCII bytes, with no terminating NUL.

## Genesis and trust bootstrap

`stream create` creates a signed genesis block, not an alternative "genesis configuration" object. Its header has `height = 0`, `previous_block_hash` set to 32 zero bytes, an empty MMR root, and exactly one `genesis` transaction.

The owner distributes this trust anchor with every join request:

```text
stream_id, genesis_block_hash, owner_signing_public_key, server_endpoint
```

A joining client must reject a chain whose genesis hash, stream ID, or owner signing public key differs from its out-of-band trust anchor. A server response alone is never a trust anchor.

## Transaction envelope

Every transaction is represented in canonical CBOR as:

```text
{
  "protocol_version": 1,
  "stream_id": bytes(32),
  "note_id": bytes(32),
  "operation_type": text,
  "operation_payload": bytes,
  "client_created_at": uint,
  "author_public_key": bytes(32)
}
```

The map above is `unsigned_body`. `operation_payload` is itself canonical CBOR for the operation schema. `note_id` is mandatory for `create` and must be 32 zero bytes for membership and key-management operations.

```text
transaction_id = SHA256("snapnotes/txid/v1" || canonical(unsigned_body))
signature = Ed25519.Sign(author_private_key,
  "snapnotes/sign/v1" || canonical(unsigned_body))
pow_preimage = "snapnotes/pow/v1" || stream_id || transaction_id ||
  author_public_key || pow_epoch_be64 || pow_nonce_be64
transaction_hash = SHA256("snapnotes/tx/v1" || canonical({
  "unsigned_body": unsigned_body,
  "transaction_id": transaction_id,
  "signature": signature,
  "pow_epoch": pow_epoch,
  "pow_nonce": pow_nonce
}))
```

`pow_epoch` and `pow_nonce` are unsigned 64-bit integers. `transaction_id` and `signature` are recomputed by every receiver and are not trusted from the wire.

The server must enforce both `transaction_id` uniqueness and `(stream_id, note_id)` uniqueness for `create` operations. Re-submitting an accepted transaction returns its original receipt; a distinct transaction reusing a `note_id` is rejected.

## Operation payloads

All payloads carry only the fields named below. Field-by-field CBOR test vectors must be committed before a networked implementation starts.

| Operation | Required payload fields | Authorisation |
| --- | --- | --- |
| `genesis` | `owner_signing_public_key`, `owner_encryption_public_key`, `initial_pow_target`, `initial_key_epoch`, `owner_key_grant` | genesis only |
| `create` | `key_epoch`, `encrypted_payload`, `payload_nonce`, `wrapped_dek`, `wrapped_dek_nonce` | active member |
| `member_add` | `device_id`, `label`, `signing_public_key`, `encryption_public_key` | owner |
| `key_grant` | `recipient_device_id`, `recipient_encryption_public_key`, `key_epoch`, `key_envelope` | owner |
| `key_rotation_bundle` | `revoked_signing_public_key`, `new_key_epoch`, `grants` | owner |

`member_revoke` and `key_rotate` are not independently accepted wire operations in v1. They are represented by one `key_rotation_bundle`; this is the atomic operation required to prevent writes under a revoked epoch. Each `grants` item has the same shape as `key_grant`.

## Encryption envelopes

`encrypted_payload` uses a random 32-byte DEK and a random 24-byte XChaCha20-Poly1305 nonce. Its plaintext is canonical CBOR containing the original text, parsed tags, note date, reminder data, and check items.

`wrapped_dek` and every `key_envelope` use this fixed recipient envelope:

1. Generate a fresh X25519 ephemeral key pair.
2. Derive `shared_secret = X25519(ephemeral_private, recipient_public)`.
3. Derive a 32-byte AEAD key with HKDF-SHA256 using an all-zero salt and the ASCII `info` value `snapnotes/key-envelope/v1`.
4. Generate a fresh 24-byte nonce and encrypt with XChaCha20-Poly1305.
5. Send `ephemeral_public_key` (32 bytes), `nonce` (24 bytes), and `ciphertext` (including its 16-byte tag).

The payload AAD is canonical CBOR of:

```text
{ "protocol_version": 1, "stream_id": stream_id, "note_id": note_id,
  "transaction_id": transaction_id, "key_epoch": key_epoch,
  "field": "encrypted_payload" | "wrapped_dek" | "key_envelope" }
```

The recipient envelope has the same AAD shape, with its applicable `note_id` (zero bytes for a stream-key grant). Nonces are never reused with the same key.

## Blocks, MMR, and sync

Before multi-node work begins, a follow-up protocol section must define the exact block-header CBOR map, `block_hash` preimage, empty MMR root, peak-bagging hash domains, and inclusion-proof encoding. "RFC 6962 style" is insufficient to implement an MMR interoperably.

For the single-node MVP, `headers` and `blocks` are inclusive of `from_height`; a client asks from its recorded height to revalidate the anchor block, then accepts only a contiguous successor chain. The server returns `409 CHAIN_MISMATCH` when the supplied `known_block_hash` at `from_height` differs. All list responses include a `next_from_height` cursor or `null`.

Every API error uses:

```json
{"error":{"code":"MACHINE_READABLE_CODE","message":"safe human-readable text"}}
```

Permanent submission errors are `400 INVALID_ENCODING`, `403 UNAUTHORIZED_KEY`, `409 DUPLICATE_NOTE_ID`, `413 PAYLOAD_TOO_LARGE`, and `422 INVALID_TRANSACTION`. Retryable errors are `409 STALE_POW_EPOCH` and `429 RATE_LIMITED`.

## Phase 5 amendment — MMR, block proofs, and multi-node verification

Status: approved on 2026-08-21; Task 5.1 (peak-bagging MMR, inclusion-proof gen/verify, byte-exact vectors, server+client migration) implemented and verified on 2026-08-22; Task 5.2 (headers-first sync, chainwork-chain selection, reorganisation with MMR-state restore + orphan labelling, malformed-peer rejection, `LastChainwork` persistence) implemented and verified on 2026-08-22; Task 5.3 (inclusion-proof serving endpoint `GET /proof` + client `VerifyLeafInclusion` multi-node cross-device verification, plus two FinalizeBlock/CatchUpHeadersFirst correctness fixes) implemented and verified on 2026-08-22.
It supersedes the "deferred to Phase 5" notes in
`internal/protocol/block.go` and defines the exact wire formats required for
interoperable MMR inclusion proofs and headers-first chain selection.

### MMR construction (peak-bagging, Polkadot-style)

The accumulator is a append-only Merkle Mountain Range over transaction leaf
hashes. Leaves are added in block order (one leaf per accepted transaction, i.e.
one leaf per block). Positions are 0-indexed.

- `LeafHash(txHash) = SHA256("snapnotes/mmr-leaf/v1" ‖ txHash)` (unchanged from MVP).
- The **empty MMR root** is 32 zero bytes. It is the root of an MMR with **0 leaves**
  and is used only as the starting sentinel before the first leaf exists. In this
  implementation the genesis block contains exactly one transaction (the `genesis`
  operation), so genesis `transaction_count = 1` and its `mmr_root` is
  `MMRRootFromPeaks([LeafHash(genesisTxHash)])` — a single peak bagged. The empty
  root never appears on a committed block.
- Each node hash is `Node(a, b) = SHA256("snapnotes/mmr-node/v1" ‖ a ‖ b)` where
  `a` and `b` are the child node hashes (left then right, each exactly 32 bytes).
- A leaf node hash is `Node(LeafHash(txHash), EMPTY)` is **NOT** used; leaves are
  treated as nodes in the peak bagging. The peak for a tree of size `n` is computed
  by the standard MMR bagging algorithm (see `MMRRoot(leaves)` below).

When a new leaf `L` is appended to an existing MMR with current peaks
`[p_0, p_1, ...]` (ordered from smallest to largest mountain), the update follows
the standard "add to the right, carry left when heights match" rule:

```text
func addLeaf(peaks, leafHash):
    node = leafHash
    i = 0
    for i in range(len(peaks)):
        if height(peaks[i]) == height(node):   # same height -> merge
            node = Node(peaks[i], node)
            i += 1
        else:
            break
    return peaks[0:i] + [node] + peaks[i+1:]    # drop the merged peaks
```

The **MMR root** is the hash of the bagged peaks:

```text
MMRRootFromPeaks(peaks) =
    SHA256("snapnotes/mmr-peak-bag/v1" ‖ peaks[0] ‖ peaks[1] ‖ ... ‖ peaks[k-1])
```

with `peaks` ordered from smallest to largest mountain. The empty root
(0 peaks) is 32 zero bytes.

`BlockHeader.mmr_root` carries `MMRRootFromPeaks(currentPeaks)` after the block's
leaf is appended. Servers MUST recompute the root from the full leaf set when
serving a proof, and clients MUST verify `mmr_root` against the bagged peaks of
all ancestor leaves they have downloaded.

### Inclusion-proof endpoint (implemented Task 5.3)

`GET /api/v1/streams/{stream_id}/proof?leaf_index=N` returns the canonical-CBOR
inclusion proof for the leaf (transaction) at 0-indexed position `N`, as a
base64url-encoded `proof` field:

```json
{ "proof": "<base64url canonical CBOR MMRInclusionProof>" }
```

The server recomputes the proof from the full ordered leaf set (`leafHashes`,
one per block including genesis) via `protocol.GenerateInclusionProof` and
serialises it with `protocol.MarshalMMRProof`. An out-of-range `leaf_index`
(≥ number of leaves) returns `404`. The client verifies it with
`SyncClient.VerifyLeafInclusion(ctx, leafIndex)`, which decodes the proof
strictly (`DecodeMMRProof`) and checks it against the device's own locally
pinned active-tip `mmr_root` (`state.LastMMRRoot`) — never a value asserted by
the server — using `totalLeaves = LastBlockHeight + 1`. This lets a client
independently prove a historical transaction belongs to the chain it trusts.

### Inclusion proof encoding

An inclusion proof for leaf at 0-indexed position `pos` (0 ≤ pos < total leaves)
is the list of sibling/peak hashes a verifier needs to reconstruct the root.

CBOR wire form (canonical map, field order as listed):

```text
{
  "leaf_index":       uint,            # 0-indexed position of the proven leaf
  "leaf_hash":        bytes(32),       # LeafHash(txHash) of the proven tx
  "peaks":            [bytes(32), ...],# summits of all mountains AFTER bagging this leaf
  "proof":            [bytes(32), ...] # sibling/upper hashes, bottom-up,
                                        # excluding peaks already in `peaks`
}
```

Verification algorithm (returns true iff the proven leaf is a member of the MMR
whose root is `claimedRoot`):

```text
func VerifyInclusion(leaf_hash, leaf_index, peaks, proof, claimedRoot, totalLeaves):
    # 1. Walk from the leaf up to the mountain summit, collecting siblings.
    idx = leaf_index
    node = leaf_hash
    sib = 0
    # number of leaves left to position at this level
    size = totalLeaves
    siblings = proof
    k = 0
    while size > 1:
        is_right = (idx % 2 == 1)
        if is_right:
            node = Node(siblings[k], node); k += 1
        else:
            # need sibling on the right; only available if not the last node
            if idx+1 < size:
                node = Node(node, siblings[k]); k += 1
            # else: this node is a peak candidate, no sibling to its right
        idx = idx // 2
        size = (size + 1) // 2
    # node is now the summit of leaf_index's mountain
    # 2. Reconstruct peak bagging and compare to claimedRoot
    allPeaks = peaks with `node` inserted at its height position
    return MMRRootFromPeaks(allPeaks) == claimedRoot
```

Notes:
- `peaks` in the wire form are the summits of **all** mountains after the leaf is
  added, so the verifier only needs `proof` for the path from leaf to its own
  summit, then inserts that summit into `peaks` and re-bags.
- A verifier MUST reject if `k != len(proof)` (extra or missing proof hashes) or
  if `leaf_index >= totalLeaves`.
- All hashes are exactly 32 bytes; `Node`, `LeafHash`, and `MMRRootFromPeaks` use
  the domain separators above.

### block_hash preimage (unchanged, reaffirmed)

`block_hash = SHA256("snapnotes/block/v1" ‖ canonical_CBOR(BlockHeader))`.

The `BlockHeader` CBOR map is unchanged from the MVP (6 fields): `protocol_version`,
`height`, `previous_block_hash`, `transaction_count`, `mmr_root`, `timestamp`. The
only semantic change in Phase 5 is that `mmr_root` is now a true peak-bagging MMR
root rather than a hash chain. The block-header CBOR map is therefore **approved
as-is**; no new fields are added.

### Chainwork and chain selection

Each block carries an implicit chainwork of `height + 1` (one unit per block) for
the single-node-compatible baseline. On multi-node sync, the active chain is the
one with the greatest `(chainwork, block_hash)` lexicographic tiebreak, **not**
merely the greatest height. A reorganisation occurs only when a candidate chain
has strictly greater chainwork than the locally active chain AND shares the
verified genesis anchor. Displaced blocks are labelled `orphaned` and their
transactions are not presented as current until re-confirmed by the new active
chain. Malformed peer data (unverifiable signature, broken `previous_block_hash`
linkage, `mmr_root` mismatch, or unknown fields) never advances local state.

### Required multi-node interoperability vectors (Task 5.1)

1. Canonical CBOR and byte-exact `MMRRoot` for a fixed leaf set of 1, 7, and 100
   transactions; empty root (32 zero bytes) for 0 leaves.
2. A byte-exact inclusion proof for leaf at position 3 of a 7-leaf MMR; altering
   the transaction hash, any sibling hash, a peak, or the bagged root MUST fail
   verification.
3. `block_hash` recomputation matches for a header whose `mmr_root` was computed
   by the peak-bagging algorithm.
4. Chain selection: a 6-block chain with chainwork 6 loses to a 5-block fork with
   chainwork 7 (constructed by PoW target difference) — the higher-chainwork fork
   becomes active.
5. Reorganisation restores `mmr_root` continuity and labels the displaced chain
   `orphaned`.
6. Malformed peer header (tampered `previous_block_hash` or non-canonical CBOR)
   is rejected without advancing local state.

## PoW epoch acceptance

Only `pow_epoch = floor(accepted_transaction_count / 1000)` is valid; there is no previous-epoch grace window. The block that accepts transaction 1000 publishes the next target for transaction 1001. This prevents stale precomputed work from remaining valid indefinitely.

The initial target and retarget equation remain as specified in `project-spec.md`. The reference device, its benchmark command, and the resulting genesis target must be recorded before a production stream is created.

## Required interoperability vectors

Before implementing network synchronization, commit test vectors for:

1. canonical CBOR bytes, transaction ID, signature, PoW preimage, and transaction hash for one `create` transaction;
2. successful and failed payload/DEK envelope decryption;
3. genesis verification against an out-of-band trust anchor;
4. duplicate `transaction_id` and duplicate `note_id` rejection;
5. PoW boundary at accepted transaction 1000; and
6. block and MMR inclusion verification once the multi-node format is approved
   (the exact format is defined in the "Phase 5 amendment" section above; the
   byte-level vectors are still pending approval of that section).
