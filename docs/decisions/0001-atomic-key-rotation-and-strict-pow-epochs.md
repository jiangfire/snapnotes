# ADR-0001: Use atomic key-rotation bundles and strict PoW epochs

## Status

Accepted

## Date

2026-08-20

## Context

The original specification required membership revocation, key rotation, and new key grants to occur without a normal-write window, but exposed only a single-transaction submission API. It also permitted an unspecified grace period for the previous PoW epoch, allowing old precomputed work to stay acceptable without a defined bound.

## Decision

Protocol v1 represents a revocation as one owner-signed `key_rotation_bundle` transaction. It contains the revoked signing key, new Stream Key epoch, and grants for every active device. The server validates and appends it atomically, then rejects both the revoked key and old-epoch future writes.

The server accepts only the epoch calculated as `floor(accepted_transaction_count / 1000)`. No previous-epoch grace period exists. The block accepting transaction 1000 publishes the target for transaction 1001.

## Alternatives considered

### Independent revoke, rotate, and grant transactions

Rejected because the single-transaction API cannot guarantee that no normal write is inserted between these operations. Pausing an unspecified interval also produces unclear recovery semantics after a crash.

### Previous-epoch PoW grace window

Rejected because a time-only or count-only window needs additional security parameters and lets stale precomputed work remain acceptable. Strict acceptance is simpler and deterministic.

## Consequences

- Phase 4 clients and servers must implement one atomic bundle operation.
- Key rotation has a bounded payload-size concern because it carries all active-member grants; any member-count limit must be specified before the product expands beyond the intended small trusted-device set.
- Clients must recompute PoW when their epoch is stale.
- The protocol and integration tests must exercise the 1000/1001 transaction boundary and concurrent post-revocation writes.
