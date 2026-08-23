# SnapNotes 下步计划（Next Steps）

Status: 2026-08-23 评审结论落地。当前 `go build/vet/test` 全绿；协议层 Phase 0–5 库与单测均已实现，但**产品层未接通**。本计划把"被测试过的库"变成"能真正多端同步的产品"。

进度：✅ **P0 已完成**（客户端密钥/流引导 + TUI 接通 sync，含真实 server 端到端测试）。✅ **P1 已完成**（服务端账本落盘，重启链与 mmr_root 一致；新增 `--data-dir`）。✅ **P2 已完成**（TUI 搜索/详情/日历/审计四页 + 导航外壳；远端笔记落盘 `ingestBlockNote`）。✅ **P3 已完成**（CLI `stream create` / `key export` / `key import`，age/scrypt 加密备份）。✅ **P4 已完成**（真实区块级 PoW + 服务端 `--peer` 节点间同步 + 外部 root 锚定）。

工作模式：TDD。每个行为先写失败测试（RED），再补实现（GREEN），最后测试清理。

## 现状缺口（速记）

- TUI 不调用任何 sync 代码；本地 `notes` 表只有 `id/body/created_at`，无链元数据。
- ~~服务端账本纯内存，重启丢全部数据（spec 14.1/14.2 要求落盘）。~~ → **P1 已落盘（SQLite：streams/blocks/leaves/members/authorized/tx_ids/note_ids）**。
- TUI 缺搜索/日历/详情/审计页；CLI 缺 `stream create` / `key export` / `key import`。
- Phase 5 残余：已全部完成（真实区块级 PoW、服务端 `--peer` 节点间同步、外部 root 锚定）。详见 P4。

## P0 — 客户端密钥/流引导 + TUI 接通 sync（立即做）

目标：让 app 创建一条笔记时，真正生成加密交易、入 outbox、提交服务端，并拉取链状态；首页显示同步状态。

- [ ] **P0.1 客户端配置与密钥引导**（`internal/client` 新包）
  - `Config` 结构：StreamID、GenesisBlockHash、OwnerSigningPublicKey、ServerEndpoint、DeviceID，以及私钥材料（Ed25519 签名私钥、X25519 加密私钥、epoch-0 StreamKey、GenesisBlock base64、PowTarget）。
  - `InitOwner(serverEndpoint)`：调用 `protocol.BuildGenesis` 生成创世与 owner 密钥，产出 `Config`（owner 设备保留私钥）；生成 DeviceID。
  - `Load/Save(path)`：JSON 落盘，文件权限 `0600`。
  - `ClientKeys()` → `sync.ClientKeys`；`TrustAnchor()` → `sync.TrustAnchor`。
  - 测试：首次 init 生成 32 字节 stream_id、非零 genesis hash、可重建 ClientKeys/TrustAnchor；Save 后 Load 字节一致。
- [ ] **P0.2 TUI 接线**（`internal/tui/home.go` + 测试）
  - `HomeModel` 持有 `Submitter`（接口，真实为 `*sync.NoteService`）、可选 `Syncer`（接口，真实为 `*sync.SyncClient`）、同步状态查询。
  - `Enter` 提交改为 `Submitter.Submit(input, now)`（内部走 `SaveNoteWithOutbox`，零网络阻塞）；提交后后台触发 `Syncer.Sync`。
  - 首页显示同步状态：存在待同步 outbox 显示 `⏳ 待同步 N`，否则 `✓ 已同步`。
  - 显示标签与未勾选项标识（spec 5.1）。
  - 测试（用 fake Submitter/Syncer）：Enter 调用 Submit 一次并清空输入；多行不拆分；提交后触发 Sync；状态展示正确。
- [ ] **P0.3 入口接线**（`cmd/snapnotes/main.go`）
  - `--data-dir`（默认 `.snapnotes`）、`--server`（默认 `http://localhost:8333`）。
  - 无 config 时 `InitOwner` 创建流并保存，打印 `snapnotes-server -genesis <base64>` 启动命令。
  - 打开 sqlite store，构建 `NoteService`（`Keys` 来自 config）、`SyncClient`（`Endpoint/Anchor/DeviceID/Repo=store`），启动期与提交后调用 `Sync`，并启定时 ticker 补拉。

验收：本地启动 app → 创建笔记 → `outbox` 出现 pending → 另起 `snapnotes-server -genesis …` → 笔记变为 synced → 重启 app 仍显示该笔记（注：本阶段服务端仍内存，重启服务端会丢；落盘见 P1）。`go test ./...`、`go vet ./...`、`go build ./cmd/...` 全绿。

## P1 — 服务端持久化（让 sync 有意义）✅ 已完成

- [x] `internal/api` 的 `Ledger` 由纯内存 map 改为落盘：采用 SQLite（`modernc.org/sqlite`，零新依赖），schema 为 `streams`/`blocks`(header_cbor+block_hash+tx_cbor 按 height)/`leaves`(leaf_hash 按 pos，peaks 启动时重算)/`members`/`authorized`/`tx_ids`/`note_ids`；每块写入在一个事务内持久化（block+leaf+状态镜像原子提交）。
- [x] 新增 `--data-dir`（默认 `.snapnotes-server`）；服务启动从磁盘重建主链、成员、epoch、MMR 状态；`-genesis` 仅在空数据目录时初始化（seed），已有数据则按盘重建。
- [x] 测试：接受交易后落盘；重启进程链与 mmr_root 一致；`/headers`、`/blocks`、`/proof` 读盘返回正确；成员/epoch/authorized 跨重启保留（`internal/api/ledger_persist_test.go`）。

## P2 — TUI 功能页

- [x] 搜索页（§5.3）：复用 `Store.SearchNotes`，支持 `tag:`/`is:unchecked`/`reminder:` 等查询语法与游标翻页。
- [x] 笔记详情页（§5.5）：正文、标签、提醒状态、作者公钥、同步状态。
- [x] 日历/提醒页（§5.4）：笔记日期与提醒日期分列；Ctrl+R 确认；每日 5 条回顾。
- [x] 链校验/审计页（Phase 5）：显示 genesis hash、last block height/hash、mmr_root、chainwork；`VerifyLeafInclusion` 入口。

## P3 — CLI 子命令（§5.6）

- [x] `snapnotes stream create --name main [--server …]`：生成密钥+创世+本地配置。
- [x] `snapnotes key export --output snapnotes-key-backup.age` / `key import --input …`：加密备份私钥（age 加密），服务端不托管。

## P4 — Phase 5 收尾 ✅ 已完成

- [x] 区块级 PoW（真实 PoW，非 height+1 隐式 chainwork）：`BlockHeader` 新增 `Nonce`/`PowTarget`；`MineBlock` 在 `BuildGenesis` 与 `tryAccept` 处挖矿；`BlockHash` 哈希含 Nonce+Target；`BlockSatisfiesTarget` 校验；客户端 `ApplyHeader` 校验自声明 PoW；服务端累计真实 `chainwork *big.Int`；`tip` 暴露 `chainwork_hex`。`BlockWork(target)` 已在 `chainwork.go`。
- [x] 服务端 `--peer` 节点间同步：`NewServerWithPeer` + `SyncFromPeer` + `ValidateAndReplaceChain` + `fetchPeerChain`（分页拉取 `/blocks`）；仅当对端累计 work 严格更大时 `replaceChainLocked` 重组；弱对端不重组。CLI 已暴露 `--peer` 标志。
- [x] 外部 root 锚定（可选）：append-only `anchors.log`（JSON-lines，模式 0600），在创世/接受/重组时追加 `{height, block_hash, mmr_root}` 检查点；重启可重读并持久化。

## 优先级说明

P0 让"库"变"产品"；P1 让产品数据可存活。两者合起来 = 能真正用起来的共享时间流。P2–P4 为体验与多节点增强，可后置。
