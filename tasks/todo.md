# SnapNotes Implementation Tasks

Status: approved for Phases 0–4 on 2026-08-20. Do not start a task until its dependencies are satisfied.

## Phase 0 — baseline

## Task 0.1: Establish the Go test baseline

**Acceptance criteria:**

- [ ] A minimal package layout and a focused `go test` command are documented in the repository.
- [ ] `GOTELEMETRY=off; go test ./...` and `go vet ./...` are clean.

**Verification:** Run the documented commands once after the baseline files are added.

**Dependencies:** None.

**Likely files:** `README.md`, first package and its `_test.go` file.  
**Scope:** Small.

## Phase 1 — local notes

## Task 1.1: Create and persist one local note

**Acceptance criteria:**

- [x] A failing test first proves creating a note assigns an ID and local creation time.
- [x] A SQLite-backed repository persists and reloads that note across a reopened database.
- [x] No network dependency exists in the create path.

**Verification:** Focused domain and SQLite integration tests; full Go suite.

**Dependencies:** Task 0.1.  
**Likely files:** up to five new domain/storage test and implementation files.  
**Scope:** Medium.

## Task 1.2: Render recent notes and submit with Enter

**Acceptance criteria:**

- [x] A test first proves Enter creates exactly one note and clears the input.
- [x] Recent persisted notes render newest first.
- [x] Pasted multiline text is one note; the documented multiline action inserts a newline.

**Verification:** Model tests plus a manual terminal interaction.

**Dependencies:** Task 1.1.  
**Likely files:** TUI model, tests, command entry point.  
**Scope:** Medium.

## Task 1.3: Parse lightweight note syntax

**Acceptance criteria:**

- [x] A failing test first covers tags, `@date`, `@remind`, `@repeat`, and checkbox parsing.
- [x] Invalid syntax preserves the original note and never prevents saving.
- [x] Parsing produces derived data and never rewrites body text.

**Verification:** Pure unit tests, including invalid and Unicode tag cases.

**Dependencies:** Task 1.1.  
**Likely files:** parser package and tests; local-note service.  
**Scope:** Medium.

## Checkpoint 1

- [x] Full suite and `go vet` pass.
- [x] Automated persistence and TUI model coverage verifies create, restart, recent ordering, and multiline input.
- [x] Human review gate cleared; search and reminders implemented in subsequent slices.

## Task 1.4: Add local full-text search and bounded results

**Acceptance criteria:**

- [x] A failing integration test proves body and tag search returns matching local notes.
- [x] Search returns at most 100 results and uses a cursor for later pages.
- [x] Results are ordered by local creation time and bounded without unbounded loading.

**Verification:** SQLite integration tests with more than 100 matching notes.

**Dependencies:** Tasks 1.1 and 1.3.  
**Likely files:** SQLite schema/repository, search service, tests.  
**Scope:** Medium.

## Phase 2 — reminders

## Task 2.1: Schedule local reminders deterministically

**Acceptance criteria:**

- [x] A failing test first covers UTC storage, configured-timezone rendering, due, overdue, and acknowledgement advance.
- [x] Reminder state is local-only; parsed reminder fields create local schedules and are not chain data.
- [x] Missed recurring reminders advance to the next future occurrence without replaying every historical reminder.

**Verification:** Unit tests use fixed instant and timezone; SQLite integration test persists local state.

**Dependencies:** Tasks 1.1 and 1.3.  
**Likely files:** reminder package, SQLite repository, tests.  
**Scope:** Medium.

## Task 2.2: Select five daily resurfacing notes

**Acceptance criteria:**

- [x] A failing test first proves due/overdue notes rank ahead of later candidates.
- [x] Selection returns no more than five and returns all candidates when fewer exist.
- [x] Acknowledge advances the configured Ebbinghaus stage deterministically.

**Verification:** Pure scheduling tests with fixed time and deterministic tie-breaking.

**Dependencies:** Task 2.1.  
**Likely files:** reminder package and tests.  
**Scope:** Small.

## Checkpoint 2

- [x] Full suite, `go vet`, and command build pass.
- [x] Integration/model coverage verifies syntax reminder persistence across reopen, status display, and Ctrl+R acknowledgement.
- [x] Human review gate cleared before protocol-dependent work.

## Phase 3 — encrypted single-node sync

## Task 3.1: Commit protocol vectors and strict wire decoder

**Acceptance criteria:**

- [x] Tests first reject unknown/duplicate fields and non-deterministic CBOR.
- [x] Approved transaction, signing, PoW, and encryption-envelope vectors pass exactly.
- [x] No network endpoint is added before these vectors pass.

**Verification:** Pure crypto/protocol tests; documented cross-implementation fixture files.

**Dependencies:** Phase 1–2 checkpoints and the approved protocol document.  
**Scope:** Medium.

## Task 3.2: Persist encrypted outbox and synchronise idempotently

**Acceptance criteria:**

- [x] A failing test first proves local creation succeeds with a stopped server.
- [x] Restart preserves the pending transaction and retries without duplicating a note.
- [x] Server rejects invalid authorisation, signature, sizes, and stale epoch with documented error codes.

**Implementation notes (2026-08-20):**

- `internal/storage/sqlite` 新增 `outbox` 表与 `SaveNoteWithOutbox`（同一本地事务写笔记+待同步交易）、`ListPendingOutbox`、`MarkOutboxSynced/Failed`。
- `internal/sync` 新增 `Transaction`/`WireTransaction`、CBOR+JSON 编解码、`BuildCreate`（DEK 加密正文、Stream Key 包装 DEK、Ed25519 签名、PoW 挖矿）、`NoteService.Submit`（本地写入+入队，零网络依赖）、`SyncClient`（幂等重发：2xx/重复→synced，STALE_POW_EPOCH/429→保留，其他→failed）。
- `internal/api` + `cmd/snapnotes-server`：POST `/api/v1/streams/{stream_id}/transactions` 校验签名、授权、大小、pow_epoch=floor(count/1000)、PoW target、txID/(stream,note) 唯一性，返回规定错误码（400/403/409 DUPLICATE_NOTE_ID/413/422/429 STALE_POW_EPOCH）。MVP 账本为内存状态。
- 已知偏离：encrypted_payload/wrapped_dek 信封 AAD 因 transaction_id 与含密文 unsigned_body 的循环依赖，使用 32 字节零值占位 transaction_id；权威绑定由 Ed25519 签名提供。待 Phase 3 评审门确认。

**Verification:** Real localhost client/server integration tests.

**Dependencies:** Task 3.1.  
**Scope:** Medium.

## Task 3.3: Catch up from a saved block anchor

**Acceptance criteria:**

- [x] A failing test first rejects a mismatched genesis or known block hash.
- [x] Client fetches a contiguous bounded page sequence and recovers after a dropped WebSocket notification.
- [x] WebSocket is notification-only; its absence never loses changes.

**Implementation notes (2026-08-20):**

- `internal/protocol` 新增 `block.go`（`BlockHeader`/`Block`/`SignedTransaction`、`BlockHash` SHA256("snapnotes/block/v1"+规范 CBOR)、`LeafHash`/`NextMMRRoot` 哈希链）与 `genesis.go`（`BuildGenesis` 用 `EncryptKeyEnvelope` 把 epoch-0 Stream Key 包装给 owner、`DecodeGenesisPayload`、`DefaultGenesisTarget`=2^240）。
- `internal/api` 重写为单节点账本：genesis 作为 `blocks[0]`，每笔被接受交易追加一个链式 `previous_block_hash` 区块且 MMR root 链式推进；新增 `GET /tip`、`/headers`（≤2000）、`/blocks`（≤100，`next_from_height` 游标，`known_block_hash` 锚点 → 409 CHAIN_MISMATCH）；`readPage` 做有界分页。
- `internal/api/ws.go` 通知型 WebSocket：仅广播 `{type:"new_block",height}` 告知“有变化”，不携带数据；连接断开不丢数据（客户端定时/启动时再拉取）。
- `internal/sync` 新增 `SyncRepository` 接口（取代 `OutboxStore`，含 `GetSyncState`/`SaveSyncState`）与 `anchor.go`：`CatchUp` 从已保存锚点分页拉取并校验 genesis 哈希、previous_block_hash 连续性、区块哈希重算、txID/签名、MMR root 链式，遇 409 返回 `ErrChainMismatch`；`Listen` 收到 `new_block` 即触发一次完整 `Sync`（flush 待发 + catch-up）。
- `internal/storage/sqlite` 新增 `sync_state` 表（`stream_id` PK、`last_block_height`、`last_block_hash`、`last_mmr_root`、`genesis_block_hash`、`device_id`）与 `SaveSyncState`/`GetSyncState`，持久化已验证链位置。
- `cmd/snapnotes-server` 现用 `protocol.BuildGenesis` 生成 genesis（或 `-genesis` base64 重载），打印信任锚点（genesis_block_hash、owner key、编码后的 genesis block）。
- 已知偏离（沿用 Task 3.2）：MVP 的 MMR 为哈希链而非真正的峰值封箱 MMR，留待 Phase 5；信封 AAD 仍用 32 字节零值 transaction_id 占位，权威绑定由 Ed25519 签名提供。

**Verification:** Local test server integration tests with forced disconnect.

**Dependencies:** Task 3.2.  
**Scope:** Medium.

## Phase 4 — multi-device authority

## Task 4.1: Join and grant a device key

**Acceptance criteria:**

- [x] A failing test first proves a valid out-of-band anchor and signed join request are required.
- [x] A granted device decrypts only the Stream Key epochs it receives.
- [x] Incorrect ephemeral key, AAD, nonce, or recipient key rejects decryption.

**Implementation notes (2026-08-20):**

- `internal/protocol/membership.go`：定义 `MemberAddPayload`、`KeyGrantPayload`、`KeyRotationGrant`、`KeyRotationBundlePayload`、`JoinRequest`、`JoinAnchor` 等协议类型（全部带 canonical CBOR tag）。`SignJoinRequest`/`VerifyJoinRequest` 用 Ed25519 对 JoinRequest 的规范 CBOR 签名/验签，并校验 stream_id 和 owner 公钥与 anchor 一致。`KeyGrantAAD` 返回带零值 transaction_id/note_id 占位的 `EnvelopeAAD`（与 BuildCreate 一致），field=`"key_envelope"`。
- `internal/protocol/membership.go`：`KeyStore` 持有 deviceID、X25519 加密私钥、按 epoch 索引的 stream key map。`ProcessTransaction` 扫描 `key_grant` 和 `key_rotation_bundle` 操作，解密发往本设备的信封，按 epoch 存储解出的 Stream Key。`StreamKey(epoch)`/`HasEpoch`/`LatestEpoch`/`DecryptWithStreamKey` 提供按 epoch 解密能力。
- `internal/sync/membership.go`：`BuildMemberAdd`、`BuildKeyGrant` 构建器（owner 签名 + PoW 挖矿），`AcceptJoin` 编排函数：先 `VerifyJoinRequest`，再用 joinReq 中的设备加密公钥构建 member_add 和 key_grant 两笔交易。
- `internal/api/server.go`：`streamState` 扩展 `owner`、`members map[string]*memberRecord`、`currentKeyEpoch`。`tryAccept` 改签名为 `(streamID, tx Transaction) (acceptResult, uint64, string)`，在锁内按操作类型校验：`member_add` 要求 owner 签名、注册成员并授权签名密钥；`key_grant` 要求 owner 签名（仅中继，信封由接收端解密）。
- 测试（`protocol/membership_test.go` + `api/server_test.go`）：`TestJoinRequestRequiresValidAnchorAndSignature`（篡改签名/错误 stream_id/错误 owner 均拒绝）；`TestKeyStoreDecryptsOnlyGrantedEpochs`（设备 A 有 epoch 0+1，设备 B 仅 epoch 0，各只能解密各自 epoch）；`TestKeyStoreRejectsTamperedEnvelope`（翻转密文/不匹配 AAD 均拒绝）；`TestAcceptJoinAuthorizesDevice`（AcceptJoin 后设备 A 注册成功、从 key_grant 恢复 Stream Key、可创建笔记 200）。
- 已知偏离（沿用 Phase 3）：信封 AAD 使用 32 字节零值 transaction_id 占位，权威绑定由 Ed25519 签名提供。

**Verification:** Protocol integration tests using independent client fixtures.

**Dependencies:** Tasks 3.1–3.3.  
**Scope:** Medium.

## Task 4.2: Revoke via atomic key rotation bundle

**Acceptance criteria:**

- [x] A failing test first proves the bundle leaves no interleaving normal-write window.
- [x] A revoked signing key cannot append after the bundle.
- [x] Active devices receive the new epoch; a revoked device retains only previously held historical keys.

**Implementation notes (2026-08-20):**

- `internal/protocol/membership.go`：`KeyRotationBundlePayload` 包含 `NewKeyEpoch`、`GrantedDevices`（新 epoch 的 key grant 列表，每项含 recipient_device_id + key_envelope）、`RevokedDeviceIDs`。`KeyRotationGrant` 为单个授权项的 CBOR 结构。`KeyStore.ProcessTransaction` 遍历 bundle 中的 grants，解密发往本设备的新 epoch 信封。
- `internal/sync/membership.go`：`BuildKeyRotationBundle` 构建器接收 `KeyRotationBundleParams`（owner 签名密钥、新 epoch、grant 规格列表、撤销设备 ID 列表、PoW epoch/target），构建单笔原子交易。该交易在服务端锁内一次性应用：无普通写入可插入撤销与 epoch 推进之间。
- `internal/api/server.go`：`tryAccept` 对 `key_rotation_bundle` 要求 owner 签名；拒绝撤销 owner 自身；拒绝不推进 epoch（`NewKeyEpoch <= currentKeyEpoch`）；在锁内执行 `delete(authorized, revoked)` → `currentKeyEpoch = NewKeyEpoch` → 为 granted 成员重新授权。这是原子操作，无交错写入窗口。
- 错误码映射：`CodeUnauthorizedKey` → HTTP 403；`CodeStreamNotFound` → HTTP 404；其他 → 422。Server 新增 `CurrentKeyEpoch()`/`IsMember()`/`AuthorizedCount()`/`MemberCount()` 查询方法及 `Server` 层委托包装。
- 测试（`api/server_test.go`）：`TestNonOwnerCannotIssueMembershipOps`（成员设备 A 尝试 member_add → 403，设备 C 未注册）；`TestKeyRotationBundleRevokesAndAdvances`（owner + A + B，bundle 撤销 B 推进 epoch 1；A 在 epoch 1 写入 200；B 新 epoch 写入 403；B 旧 epoch 写入 403；B 保留 epoch-0 key 可读历史但无 epoch-1 key）。
- 已知偏离（沿用 Phase 3）：信封 AAD 零值 transaction_id 占位。`member_revoke`/`key_rotate` 不是独立线协议操作，统一由 `key_rotation_bundle` 原子表达。

**Verification:** Multi-client integration test with concurrent attempted writes.

**Dependencies:** Task 4.1.  
**Scope:** Medium.

## Phase 5 — multi-node verification (approval gated)

## Task 5.1: Define and test MMR/block vectors

**Acceptance criteria:**

- [x] Approved block-header, MMR root, and inclusion-proof fixtures pass byte-for-byte.
- [x] Altering a transaction, sibling hash, peak, or root fails verification.

**Implementation notes (2026-08-22):**

- `internal/protocol/mmr.go`（NEW）：真正的峰值封箱 MMR（Polkadot 风格）。`LeafHash(txHash)=SHA256("snapnotes/mmr-leaf/v1"+txHash)`（保留在 block.go，未重复定义）；`Node(a,b)=SHA256("snapnotes/mmr-node/v1"+a+b)`；`MMRRootFromPeaks(peaks)=SHA256("snapnotes/mmr-peak-bag/v1"+peak0+peak1...)`（按升序峰值封箱）或 0 叶根 = 32 零字节；`AddLeaf(peaks,leafHash,leafCount)` 沿 `leafCount` 的置位 climbing 合并；`mountainStart`/`merkleRoot`/`mountainSummits` 提供山形完美树辅助。
- `MMRInclusionProof{LeafIndex,LeafHash,Peaks,Proof}` 及 `GenerateInclusionProof(leaves,pos)`/`VerifyInclusionProof(p,claimedRoot,totalLeaves)`：证明从叶爬到所在山峰顶，验证时按高度替换对应峰值并重新封箱；越界 `LeafIndex`、`len(Peaks)==0`、层级数不符均拒绝。`MarshalMMRProof`/`DecodeMMRProof` 使用 `CanonicalMarshal`/`StrictDecode`（拒绝未知/重复字段）。
- `internal/protocol/mmr_test.go`（NEW）：固定字节级脚手架（`deterministicLeaf`、`buildLeaves`）+ 钉死向量 `root1/root7/root100` 与 `proof7` 峰/证明哈希；`TestMMRRootByteExact`、`TestMMRRootMatchesAddLeaf`、`TestInclusionProofByteExact`、`TestInclusionProofRoundTrip`（1/2/3/5/7/8/13/100 叶 × 所有位置）、`TestInclusionProofTamper`（篡改叶/兄弟/峰值/根、越界索引、错误总数全部失败；未改动则通过）、`TestMMRProofMarshalRoundTrip`。
- 协议文档 `docs/protocol-v1.md`：增补"Phase 5 amendment"章节（峰值封箱 MMR、包含证明线格式+验证算法、chainwork、6 个多节点向量），并修正 C1 自相矛盾——澄清 genesis `transaction_count=1`、`mmr_root=MMRRootFromPeaks([LeafHash(genesisTxHash)])`，空根仅作 0 叶哨兵。
- 服务端/客户端迁移至封箱 MMR：`internal/protocol/genesis.go` 用 `MMRRootFromLeaves([LeafHash(txHash)])`；`internal/protocol/block.go` 删除无用 `NextMMRRoot`；`internal/api/server.go` 在锁内维护 `peaks` 并以 `MMRRootFromPeaks` 计算 `mmr_root`，同时加入 M4 的 PoW epoch 锁内校验、M5 的 `key_grant` 严格解码与字段校验、M3 的 rotation bundle 失败即拒（每笔 grant 必须指向成员或 owner）。`internal/storage/sqlite/anchor.go`+`store.go` 新增 `last_peaks` 列；`internal/sync/anchor.go` 的 `CatchUp` 改为维护 `peaks`（从 `splitPeaks(state.LastPeaks)` 恢复），逐块校验 `MMRRootFromPeaks(peaks)==header.MMRRoot`。
- 全程 `GOTELEMETRY=off go build/vet/test ./...` 全绿；Phase 3.3 catch-up 测试仍通过（服务端写路径与客户端验证路径一致迁移）。
- 遗留偏离（待后续评审门/Phase 5.2 处理，非 5.1 阻塞）：C2 信封 AAD `transaction_id` 零值占位；M1 服务端/客户端未在接收线数据上跑 `DecodeUnsignedBody`/`DecodeBlockHeader` 严格解码（当前 JSON unmarshal 静默丢弃未知字段）；M2 客户端 `CatchUp` 未调用 `protocol.DecodeBlockHeader`；N1 死测试 `TestGenesisAnchorMismatchIsDetectable` 无断言。

**Verification:** Pure protocol-vector tests.

**Dependencies:** Approved multi-node protocol amendment.  
**Scope:** Medium.

## Task 5.2: Headers-first peer synchronisation and reorganisation

**Acceptance criteria:**

- [x] A failing test first proves chainwork, not height, chooses the active valid chain.
- [x] Reorganisation restores MMR state and labels displaced records orphaned.
- [x] Malformed peer data never advances local state.

**Implementation notes (2026-08-22):**

- `internal/protocol/chainwork.go`（NEW）：chainwork 原语。`BlockWork(target []byte)=floor((2^256-1)/(target+1))`（对所有范围内 target 等价于 `floor(2^256/(target+1))`，但保持在 32 字节内）、`CumulativeChainwork(...)` 求和、`SelectBestChain(aWork,aTip,bWork,bTip)`（先比 chainwork，再按 `(chainwork, block_hash)` 字典序 tiebreak；相等不算strictly better）、`bytesGreater`。
- `internal/sync/chain.go`（NEW）：`ChainManager` + `ChainStore` 接口 + `memChainStore`（按 hash 建索引，同一高度可共存 fork 头）。`SeedGenesis`/`SeedActive` 恢复根；`ApplyHeader` 校验重算区块哈希、genesis/previous_block_hash 链接、按 `targetAt(h)` 累积 chainwork；`FinalizeBlock` 校验 txID/签名/stream + `MMRRootFromPeaks(peaks)==header.MMRRoot`（`verify` 失败则 `removeHeader` 不推进状态）；`recomputeActive` 走最佳 tip 回退到 genesis 标记 active，其余 orphaned；`OrphanedHeaders` 返回被 displace 的头。
- `internal/sync/chain_test.go`（NEW）：向量测试——`TestChainManagerReorgLabelsOrphanedAndRestoresMMR`（向量5：更长但链工作量更小的 fork 被重组，active tip 的 MMR 根恢复到所选 fork）、`TestChainManagerGreaterChainworkWinsRegardlessOfHeight`（向量4：更短但目标更难→链工作量更大的 fork 胜出，即便高度更小）、`TestChainManagerRejectsTamperedHeader`（向量6：翻转 prev_hash 被拒且不推进）、`TestChainManagerRejectsMMRMismatch`、`TestChainManagerRejectsOutOfOrderHeaders`、`TestDecodeBlockHeaderRejectsUnknownField`（非规范 CBOR map 含 `bogus_field` 被拒）。
- `internal/sync/catchup_headers.go`（NEW）：`CatchUpHeadersFirst`（Phase 5 头优先同步）。先拉 `/headers` 全量、`ApplyHeader` 逐头跑链选择/重组（按 chainwork，非 height）；header 阶段后立即把 active tip（含 `LastChainwork`）持久化到 `sqlite.SyncState`；随后只 finalize active 链上本地未 finalize 的 block（`GetByHeight` 偏好 active，自动跳过 orphaned fork）。Malformed 头/不共享信任锚 genesis 的链一律不推进状态并返回 `ErrChainMismatch`/`ErrMalformedHeader`。
- `internal/storage/sqlite/anchor.go`：`SyncState` 新增 `LastChainwork []byte`（32 字节大端）；`SaveSyncState`/`GetSyncState` SQL 增加 `last_chainwork` 列读写。
- `internal/storage/sqlite/store.go`：`sync_state` 建表加 `last_chainwork BLOB`。
- `internal/sync/catchup_headers_test.go`（NEW）：`TestCatchUpHeadersFirstDownloadsChainAndPersistsChainwork`（端到端 headers-first 同步成功、持久化 `LastChainwork` 非零且 32 字节、幂等重跑不回退/chainwork 不变）、`TestCatchUpHeadersFirstRejectsMismatchedGenesis`（不匹配 genesis 不持久化任何状态）。
- 全程 `GOTELEMETRY=off go build/vet/test ./...` 全绿（`internal/sync` 8.0s、`internal/api` 4.8s、`internal/storage/sqlite` 5.0s、其余 cached）。
- 遗留偏离（非 5.2 阻塞，沿用 Phase 3/5.1，待后续评审门）：C2 信封 AAD `transaction_id` 零值占位；M1 服务端/客户端未在接收线数据上跑 `DecodeUnsignedBody`/`DecodeBlockHeader` 严格解码；M2 客户端 legacy `CatchUp` 未调用 `protocol.DecodeBlockHeader`；N1 死测试 `TestGenesisAnchorMismatchIsDetectable` 无断言。`CatchUpHeadersFirst` 在 header 阶段依赖 `ApplyHeader` 的重算哈希校验（等价于严格解码意图），但区块交易体仍由 `decodeWireBlock`（JSON）解码，`M1` 的彻底严格解码留待统一整改。

**遗留偏离整改（2026-08-22 第二轮）：**

- **M1/M2 已修复**：所有信任边界 JSON 解析点启用 `json.Decoder.DisallowUnknownFields()`，杜绝"JSON 静默丢弃未知字段"——`UnmarshalWireJSON`（`transaction.go`，提交接收侧）、`anchor.go` 与 `catchup_headers.go` 的 client page 解码（header/block 接收侧）。新增回归测试 `TestUnmarshalWireJSONRejectsUnknownField`、`TestCatchUpHeadersFirstRejectsUnknownHeaderField`（tamper server 注入未知字段 → 拒绝且不持久化状态）。注意：M1/M2 的本质是"未知 JSON 字段被拒"（wire 为 JSON，无法跑 CBOR 严格解码）；CBOR 严格解码（`DecodeUnsignedBody`/`DecodeBlockHeader`）仍覆盖 CBOR 传输路径（outbox/txCBOR/block CBOR）。
- **N1 已修复**：`TestGenesisAnchorMismatchIsDetectable` 重写为真实断言——篡改的 genesis anchor 与重算的 `GenesisBlockHash` 不等（即 mismatch 可被检测），并指明上层 `ChainManager.ApplyHeader` 在 height 0 时据此返回 `ErrChainMismatch`。
- **C2 评估结论 — 非缺陷，有意设计偏差**：`KeyGrantAAD` 的 `transaction_id`（及 `NoteID`）零值占位源于循环依赖——key_envelope 内嵌于 `OperationPayload`，而 `transaction_id` 由整条 `UnsignedBody`（包裹该 payload）派生；填入真实 id 需"先有 id 后有 body"。权威绑定由 Ed25519 签名（覆盖整条 body）+ 其余 AAD 字段（StreamID/KeyEpoch/Field/NoteID）承担；`TestKeyEnvelopeRejectsAADAndRecipientTampering` 已验证 AAD 任一字段被改即解密失败。本轮在 `membership.go` 注释补全该论证，未改代码。

**Verification:** Multi-node local integration tests + protocol chainwork unit tests.

**Dependencies:** Task 5.1.  
**Scope:** Medium.

**TUI 接线（2026-08-22 第三轮，用户选「先1后2」）：**

- **接线点**：`internal/sync/client.go` 的 `Sync` 函数——catch-up 阶段由 `c.CatchUp(ctx)` 改为 `c.CatchUpHeadersFirst(ctx)`。headers-first 是 legacy `CatchUp` 的 superset（先同步全量 header 选链 + 重组，再 finalize active chain block），且保留 `Sync` 所需语义：网络不可达返回 nil（不改 anchor）、`ErrChainMismatch` 冲突、损坏状态不推进。`CatchUp` legacy 函数保留不动（`catchup_test.go` 仍直接测它），仅从 `Sync` 解耦。
- `internal/sync/anchor.go` 的 `Listen` 注释由「trigger a local CatchUp」改为「trigger a local Sync」并指明 Sync 走 headers-first；`Listen` 本身不改动（通过 `Sync` 自然走新路径）。
- **新增回归测试**：`internal/sync/catchup_headers_test.go` 的 `TestSyncRoutesThroughHeadersFirst`——锁定「Sync 走 headers-first」：验证 `Sync` 后持久化的 `LastChainwork` 非零（只有 `CatchUpHeadersFirst` 会写入累积 chainwork；legacy `CatchUp` 不写该字段），且幂等重跑不回归、chainwork 不变。
- **验证**：`GOTELEMETRY=off go build ./...` 绿；`go vet ./internal/sync/` 绿；`go test ./internal/sync/ ./internal/api/` 全绿（sync 22.7s、api 8.5s）。
- **下一步（选项2，用户同意接着做）**：客户端对收到的 `txCBOR`/block CBOR 显式跑 `DecodeBlock`/`DecodeUnsignedBody`，彻底化 M1 的 CBOR 路径（目前靠 JSON 字段校验 + `FinalizeBlock` 哈希校验兜底）。

**选项2 — /blocks 改为真正发 CBOR 块（2026-08-22 第四轮，用户选「把 /blocks 改为真正发 CBOR 块」）：**

- **前置事实澄清**：原 `/blocks` 返回 JSON（`{header json, block_hash, transaction json}`），transaction 由服务端 `txToWireJSON` 把内部 CBOR `rec.txCBOR` 转 JSON 发给客户端；客户端 `decodeWireBlock` 用 `UnmarshalWireJSON`（JSON）解。客户端收到的根本不是 CBOR——之前记的「客户端收 CBOR 跑 DecodeBlock」前提不成立，这是 wire 协议形态变更而非补洞。用户确认直接把 `/blocks` 改为发 CBOR 块。
- **服务端 `internal/api/server.go`**：
  - `blockEntry` 由 `{header, block_hash, transaction json}` 改为 `{block: base64url(CBOR block)}`——整个块（header+hash+tx）作为单一 canonical CBOR item（`protocol.MarshalBlock`）。
  - 新增 `marshalBlock(rec blockRecord)`：`notesync.DecodeSignedTransaction(rec.txCBOR)` 取 `protocol.SignedTransaction` 再 `protocol.MarshalBlock`。
  - 删除 `txToWireJSON`（仅服务于旧 blocks JSON，已无调用点）。
  - 新增错误码 `CodeInternal = "INTERNAL_ERROR"`（marshal 失败兜底）。
  - `readPage` 的 `withTx` 分支改用 `marshalBlock`。
- **`internal/sync/transaction.go`**：新增导出 `DecodeSignedTransaction(data []byte) (protocol.SignedTransaction, error)`，供服务端把存储 CBOR 重包成块。
- **客户端 `internal/sync/anchor.go`**：
  - `wireBlock` 由 `{Header, BlockHash, Transaction json}` 改为 `{Block string}`。
  - 删除 `decodeWireBlock`，新增 `decodeCborBlock(wb wireBlock) (protocol.Block, error)`：base64 解码 + `protocol.DecodeBlock`（严格解码，拒非 canonical/未知字段/尾部数据，闭合 M1 CBOR 接收边界）。
  - legacy `CatchUp` 循环改用 `decodeCborBlock` 得 `blk`，字段访问改为 `blk.Transaction.UnsignedBody/...`（`AuthorPublicKey` 在 `UnsignedBody` 内，非顶层，已修正）。
- **客户端 `internal/sync/catchup_headers.go`**：`fetchBlock` 改用 `decodeCborBlock(page.Blocks[0])` 直接返回 `protocol.Block`（去掉中间 `toProtocolSignedTransaction` 转换；该函数仍被 `DecodeSignedTransaction` 使用，保留）。
- **测试**：
  - `internal/api/server_test.go` 的 `TestBlocksPaginationIsBoundedAndContiguous` 改为对每页 block 用 `base64.RawURLEncoding.DecodeString` + `protocol.DecodeBlock` 取 header/hash 验证连续性。
  - `internal/sync/chain_test.go` 新增 `TestDecodeCborBlockRejectsMalformed`——钉死 M1 CBOR 接收严格解码：合法 `MarshalBlock` 块可解；带 `bogus_field` 未知字段的块、尾部垃圾字节的块均被 `decodeCborBlock` 拒绝。
- **验证**：`GOTELEMETRY=off go build/vet/test ./...` 全绿（sync 15.6s、其余 cached）。端到端 `TestSyncRoutesThroughHeadersFirst` 与 `TestCatchUpHeadersFirstDownloadsChainAndPersistsChainwork` 已间接证明真实 `/blocks`→`fetchBlock`→`decodeCborBlock`→`DecodeBlock` 链路工作。M1 的 CBOR 路径彻底化完成：服务端接收提交（CBOR `UnmarshalTransaction`）、本地 outbox（CBOR `DecodeTransaction`）、**客户端接收块（CBOR `DecodeBlock`）**三处 CBOR 信任边界均严格解码。
- **非阻塞可选**：`/headers` 仍可评估是否 CBOR 化（当前 JSON header 选链轻量，收益低，保持）。

---

**决策记录 — /headers 不迁移到 CBOR（2026-08-22 第五轮，用户确认"算了"）：**

- **评估结论**：保持 `/headers` 为 JSON，**不做** CBOR 化。M1 的 CBOR 严格解码目标在 `/blocks` 改造后已完全达成（服务端收提交、本地 outbox、客户端收块三处 CBOR 信任边界均严格解码）。
- **理由（基于代码事实）**：
  1. `/headers` 已用 `json.Decoder.DisallowUnknownFields()` 严格解析，对"标量字段 + 两个 base64 哈希"的内容严格度等同于 CBOR 严格解码。
  2. header 不含复合签名/AAD 结构（那是 transaction 的职责），所以"JSON 静默丢字段"的风险敞口在 header 阶段不存在——这正是 `/blocks` 改 CBOR 的动机，对 header 不适用。
  3. header 的密码学绑定来自服务端重算的 `block_hash` + `ChainManager.ApplyHeader`（prev_hash 连续性、genesis 匹配），与 wire 编码无关。
  4. 改造成本真实且非零：需新增 `protocol.MarshalHeader`/`DecodeHeader`（canonical+strict）、改写服务端 `headerEntry`/`toWireHeader` 与客户端 `headerPage`/`headerEntryLocal`/`decodeWireHeader`/`wireBlockHeader`、把 `TestCatchUpHeadersFirstRejectsUnknownHeaderField` 从"注入 JSON 未知字段"改为"注入畸形 CBOR"、破坏性 wire 变更——且**无任何安全增益**。
- **重审触发条件**：仅当 (a) 整个同步 wire 协议有意统一为 CBOR（为未来二进制/带宽优化铺路），或 (b) header 开始携带复合结构（如 transaction 摘要，那时 JSON 静默丢字段才成为真实风险）时，再回头做。
- 已同步记入 `tasks/plan.md` 的 Decision log。

---

**Task 5.3 — MMR inclusion-proof 接线 + 跨节点验证（2026-08-22 第六轮，用户选「接线 inclusion proof + 跨节点验证测试」）：**

Phase 5 标题是 "multi-node verification"，但 5.1/5.2 完成后，`GenerateInclusionProof`/`VerifyInclusionProof`/`MarshalMMRProof`/`DecodeMMRProof` 仅库+单测，服务端无 proof 端点、客户端未用它验证祖先叶——这是真正未落地的核心。本轮闭环。

- **服务端 `internal/api/server.go`**：
  - `streamState` 新增 `leafHashes [][]byte`（有序叶哈希，每块一个含 genesis）；`NewLedger` 初始化 `leafHashes: [LeafHash(genesisTxHash)]`；`tryAccept` 每次接受追加 `LeafHash(txHash)`。
  - 新增 `GET /api/v1/streams/{id}/proof?leaf_index=N` 端点（`handleProof` + `proofFor`）：越界返回 404，否则 `GenerateInclusionProof(st.leafHashes, N)` → `MarshalMMRProof` → base64 返回 `{proof: "<b64 cbor>"}`。
- **客户端 `internal/sync/client.go`**：新增 `VerifyLeafInclusion(ctx, leafIndex)`——GET `/proof` 拿 CBOR proof，`DecodeMMRProof` 严格解码，用本地已验证 `state.LastMMRRoot`（claimedRoot）+ `LastBlockHeight+1`（totalLeaves）调 `VerifyInclusionProof`。无 Sync 状态返回错误；越界/服务端拒收返回 error。
- **测试 `internal/sync/catchup_headers_test.go`**：`TestCrossNodeVerifyLeafInclusion`——节点A 构建链（genesis+5），独立节点B 从 A `Sync` 后，对 leaf 0/2/5 验证为 true；篡改 proof 的 `LeafHash` 一字节 → 验证失败；越界索引与无 Sync 状态均被正确拒绝。把 multi-node verification 真正钉死。
- **修复两个潜伏 bug（接线时暴露）**：
  1. `ChainManager.FinalizeBlock` 原用 `stored.Peaks`（ApplyHeader 阶段的陈旧快照）累加 MMR，导致客户端累积 root 与服务端不符（之前因 legacy 测试不断言 `LastMMRRoot` 而潜伏）。改为基于**已 finalize 的 parent header 的 Peaks** 累加。
  2. `CatchUpHeadersFirst` 原在 finalize **之前** persist header-phase tip，导致 finalize 重新读回该 state 把 start 推到 tip 高度、跳过所有低位块；resume 时新 ChainManager 重建、parent Peaks 陈旧而验证失败。改为 **先 finalize 后 persist**，且每次同步都从 height 0 重推导 Peaks（幂等 guard 保证已 finalize 块为 no-op）。
- **验证**：`GOTELEMETRY=off go build/vet/test ./...` 全绿（sync 14s、api 5.6s）。`TestCrossNodeVerifyLeafInclusion` 等全过。
- **文档**：`tasks/plan.md`（status 行 + 新增 Task 5.3 checkpoint）、`docs/protocol-v1.md`（status 行 + 新增 Inclusion-proof endpoint 小节）、本日志同步更新。
