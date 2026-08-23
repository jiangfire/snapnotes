# SnapNotes Threat Model

Status: accepted for Phases 3–4 on 2026-08-20; review again before Phase 5 multi-node work.

## Assets

- note plaintext, tags, dates, reminders, and check items;
- device signing and encryption private keys, Stream Keys, and encrypted backups;
- membership and revocation state;
- integrity and availability of the append-only history.

## Trust boundaries

| Boundary | Untrusted input | Required control |
| --- | --- | --- |
| TUI to local domain | text, pasted multiline content, local clock | parser is permissive; bound text size; persist before network work |
| Local SQLite/keyring | device files and backups | parameterized SQL; OS permissions/keyring; never log key material |
| Client to server | JSON transactions, WebSocket notices, block data | TLS, size/time limits, strict decoding, signature/PoW/authorisation checks |
| Peer to peer (future) | headers, blocks, advertised endpoint | validate every block and MMR root; do not treat peer data as a trust anchor |
| Owner to joining device | join request and genesis anchor | verify join signature; confirm trust anchor out of band |

## Primary abuse cases and required tests

| Abuse case | Mitigation | First test |
| --- | --- | --- |
| Modified ciphertext or cross-stream replay | signature plus AEAD AAD binding | reject altered ciphertext and changed stream ID |
| Replay of an accepted request | stable transaction ID receipt | repeated submission produces no second note |
| Different transaction reuses a note ID | `(stream_id, note_id)` uniqueness | reject the second create |
| Revoked device writes with an old Stream Key | atomic `key_rotation_bundle` and epoch check | reject post-bundle old-key create |
| Malicious server supplies a new history | out-of-band genesis anchor and saved hash | reject mismatched genesis/known block hash |
| Oversized/malformed request consumes resources | body caps and strict CBOR/JSON decode | reject before storage or expensive PoW work |
| Offline database copied from an unlocked device | explicit local-device threat-model limitation | documentation assertion; encryption at rest is not claimed |

## Security invariants

- Private keys, Stream Keys, plaintext payloads, and decrypted backup material must never enter logs, ordinary configuration, telemetry, or server responses.
- The server authorises from chain-derived membership state only; it never accepts a client-supplied privilege, height, sequence, or root as authoritative.
- External data is validated exactly once at API and file boundaries; internal typed data is not repeatedly reparsed.
- All network-facing handlers have bounded request bodies, deadlines, and a stable non-secret error shape.
- A single-node MVP provides tamper evidence relative to a saved anchor, not Byzantine-safe consensus.

## Explicit non-goals

- E2EE does not protect plaintext on an unlocked endpoint.
- Revocation does not retract historic Stream Keys already obtained by a device.
- The single-node MVP does not prevent a malicious server from withholding availability or rewriting history before a client has an external/saved anchor.
