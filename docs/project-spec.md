# SnapNotes 项目 Spec

版本：0.2 协议草案  
状态：已评审；Phase 0–4 设计已批准，Phase 5 的多节点协议待补充  
更新时间：2026-08-18

## 1. 产品概述

SnapNotes 是一个基于 Go 和 Bubble Tea 的本地优先（Local-first）共享时间流 TUI。

它的核心目标是：任意授权终端想到一个点子后，可以在几秒内追加到共享时间流；所有其他授权终端都能同步、查看和搜索这些消息。

产品不追求复杂排版，也不建立独立的“任务”对象。所有内容统一称为“笔记”，笔记正文中可以包含少量标签、日期、提醒和勾选项语法。

## 2. 产品目标

### 2.1 目标

- 快速记录：启动后优先展示最近笔记，底部始终保留输入框。
- 本地可用：断网时仍可创建、浏览、搜索和设置提醒。
- 多端同步：多个终端共同订阅同一条时间流，断线后可以按区块自动补偿。
- 低格式负担：以纯文本为主，支持有限的轻量语法。
- 可回顾：支持全文搜索、标签筛选、日期筛选、日历和随机 resurfacing。
- 端到端加密：笔记正文和敏感元数据在终端加密，服务端只保存密文交易。
- 可信写入：授权终端使用与登记公钥对应的私钥签名写入。
- 区块链式存储：笔记以签名交易追加到区块链账本，使用工作量证明限制写入和防止重复提交。
- 可验证同步：所有终端同步区块头和区块内容，并验证链的完整性。

### 2.2 非目标

以下能力永远不包含在产品范围内：

- 富文本编辑器。
- 图片和附件。
- 复杂排版。
- 多人协作编辑同一条笔记。
- 复杂的标签层级和知识图谱。

多个授权终端可以向同一个服务端追加不同消息，并同步同一条时间流；这不属于多人协作编辑。

## 3. 用户与使用场景

### 3.1 目标用户

一个用户控制的多个终端。第一版允许用户主动批准少量受信任终端加入同一条加密时间流；这不是开放注册或陌生人协作产品。

### 3.2 核心场景

1. 用户想到一个点子，启动或切回 SnapNotes，在底部输入框输入后按 `Enter`，笔记立即保存。
2. 用户打开应用，浏览最近笔记流，查看刚才记录过的内容。
3. 用户通过关键词、标签、时间或提醒状态查找旧笔记。
4. 用户为一条笔记设置指定时间或重复提醒。
5. 用户离线记录，联网后后台自动同步。
6. 其他授权终端使用自己的私钥签名，并向共享时间流追加一条所有成员可见的笔记。

## 4. 核心概念

### 4.1 共享时间流

共享时间流是产品的顶层对象。第一版默认只创建一条主时间流。协议支持未来在同一个服务端承载多条时间流，但每条时间流拥有独立的链、创世区块、成员状态、密钥 epoch 和 MMR；不同时间流之间不共用区块或顺序号。

时间流拥有成员公钥列表、密钥 epoch、独立的区块链账本和同步状态。授权终端加入时间流后，可以读取被授予密钥的历史消息并追加新消息。写入权限要求持有已登记签名公钥对应的私钥；仅知道公钥不能写入。

### 4.2 笔记

笔记是时间流中的一条消息。每条笔记创建时生成不可变的 `note_id` 和 `transaction_id`。流内顺序号由服务端在交易进入主链后按区块顺序分配，客户端离线期间只能保存本地创建时间，不能预分配全局顺序号。笔记默认只允许追加，不允许原地编辑。

内容一旦写入不可修改或删除；如果需要补充或修正，重新创建一条独立笔记。

### 4.3 勾选项

勾选项是笔记正文中的一种 Markdown 风格语法，不是独立的产品对象：

```text
- [ ] 研究 N100 的功耗
- [x] 查阅树莓派资料
```

### 4.4 标签

标签是笔记正文中以 `#` 开头的标记，例如 `#idea`、`#golang`。标签经过解析后建立本地索引，但正文仍然是唯一真源。

### 4.5 提醒

提醒是附着在笔记上的调度信息，可以来自正文语法，也可以由 TUI 操作生成。提醒不改变笔记本身的语义。

## 5. TUI 信息架构

### 5.1 首页：最近笔记

首页默认显示按主链 `stream_sequence` 倒序排列的最近笔记流。客户端尚未同步的笔记暂时显示在顶部，并标记为本地待同步；同步后按服务端分配的顺序重新定位。客户端创建时间只用于显示和查询，不决定跨终端的全局顺序。

```text
┌─ Recent Notes ─────────────────────────────────┐
│ 10:42  想做一个终端版闪念笔记                   │
│        #idea #golang                           │
│                                                │
│ 09:15  研究低功耗服务器                        │
│        #homelab  reminder: tomorrow            │
├────────────────────────────────────────────────┤
│ > 输入新的闪念...                         Enter │
└────────────────────────────────────────────────┘
```

首页需要显示：

- 创建时间和相对时间。
- 笔记正文预览。
- 标签。
- 提醒状态。
- 是否包含未勾选项。
- 同步状态或本地未同步标识。

本地提醒存在时，首页在笔记下方显示 `reminder: active/due/overdue/dismissed`。使用 `↑/↓` 选择笔记，`Ctrl+R` 确认当前选中笔记的提醒；重复提醒推进到下一次计划时间，一次性提醒变为 `dismissed`。

### 5.2 底部输入框

- `Enter`：立即创建笔记。
- `Ctrl+Enter` 或 `Alt+Enter`：插入换行。
- `Esc`：第一次按下清空当前输入内容，输入为空时退出输入状态；不删除已经保存的笔记。
- 粘贴多行内容时，不应把每一行拆成多条笔记。
- 每次提交都是时间线中的一条新笔记。

终端对 `Ctrl+Enter`、`Alt+Enter` 的上报方式可能不同，第一版提供可配置快捷键和“多行输入模式”备用入口；至少保证存在一种稳定的换行操作。

### 5.3 搜索页

搜索页包含：

- 搜索输入框。
- 结果列表。
- 标签筛选。
- 时间范围筛选。
- 提醒状态筛选。
- 勾选项文本和正文全文搜索。

建议支持以下查询语法：

```text
服务器
tag:idea
服务器 tag:idea created-after:2026-08-01
date-before:2026-09-01 has:reminder
is:unchecked
reminder:overdue
```

查询字段语义固定如下：`created-after/before` 查询客户端记录时间，`included-after/before` 查询进入主链的时间，`date-after/before` 查询 `@date`，`remind-after/before` 查询提醒时间。日期参数使用客户端时区解释；带时间的参数必须是 RFC3339。

搜索默认在本地解密后的索引上执行，结果按 `stream_sequence` 倒序分页。服务端不建立正文全文索引，只按区块高度和同步元数据工作。单次本地查询最多返回 100 条，继续翻页必须使用游标，不能无界加载全部结果。

### 5.4 日历与提醒页

日历页按日期展示两种信息：

- 笔记日期：笔记创建或用户指定的日期。
- 提醒日期：未来需要重新提醒用户的时间。

日历日期和提醒时间必须分开存储，避免将“这条笔记属于哪天”和“什么时候提醒我”混为一谈。

### 5.5 笔记详情页

详情页支持：

- 查看完整正文。
- 查看标签。
- 查看提醒到期状态。
- 查看作者终端和同步状态。

笔记创建后不可编辑、不可删除。内容需要修正时，重新创建一条新笔记；第一版不建立笔记之间的修正关系。

### 5.6 CLI 子命令

共享时间流通过 CLI 创建：

```text
snapnotes stream create --name main
snapnotes stream create --name main --server https://example.invalid
```

该命令负责：

- 生成 Stream Owner 的签名密钥和加密密钥。
- 创建唯一的、已签名的创世区块和创世交易。
- 初始化本地 SQLite、密钥和 MMR 状态。
- 如果提供 `--server`，将共享时间流注册到服务端。
- 私钥不会自动上传或备份到服务端。

密钥备份由用户自行负责。可以使用以下命令导出和恢复加密密钥包：

```text
snapnotes key export --output snapnotes-key-backup.age
snapnotes key import --input snapnotes-key-backup.age
```

备份文件必须由用户自行保存；服务端不提供托管备份。私钥和备份同时丢失时，历史密文无法恢复。

后续终端通过加入流程获得授权，不直接修改服务端的公钥列表。

## 6. 轻量文本语法

第一版建议支持：

```text
#idea #golang
@date 2026-08-18
@remind 2026-08-20T09:00:00+08:00
@repeat weekly

- [ ] 需要继续确认的内容
- [x] 已经完成的内容
```

解析原则：

- 语法是可选的，普通文本永远合法。
- 解析失败时保留原文，不阻止笔记保存。
- 解析结果是派生数据，不覆盖正文。
- 系统字段在创建笔记时由 TUI 生成，不要求用户手写语法。
- `@date` 只表示用户给笔记归属的日历日期，格式为 `YYYY-MM-DD`，不包含时区。
- `@remind` 必须使用 RFC3339 时间并带时区偏移；客户端解析后以 UTC 保存。
- `@repeat` 只允许 `daily`、`weekly`、`monthly` 或 `every:Nd`，第一版不支持自然语言日期。

## 7. 标签体系

第一版使用扁平标签：

- 标签统一规范化为小写。
- 支持中文、英文、数字、连字符和下划线。
- 同一条笔记中的重复标签自动去重。
- 标签不区分层级。
- 标签列表按使用次数和最近使用时间排序。
- 删除标签只删除索引，不修改历史正文。

本地派生表：

```text
note_tags
- note_id
- tag
- source_transaction_id
```

未来如果确实需要，可以增加标签别名或层级，但不提前加入核心模型。

## 8. 提醒机制

提醒分为四类：

1. 指定时间提醒：例如 `2026-08-20 09:00`。
2. 重复提醒：每天、每周、每月或自定义间隔。
3. 间隔回顾：每天选择 5 条笔记，使用艾宾浩斯式间隔安排回顾。
4. 勾选项回顾：包含未完成勾选项的笔记进入待回顾列表。

提醒字段分为两类。笔记正文中的 `@remind` 和 `@repeat` 属于加密 payload，会随笔记同步；TUI 通过详情页创建的额外提醒属于当前终端的本地状态，不写入链上，也不自动同步到其他终端。

本地提醒表使用以下字段：

```text
local_reminders
- reminder_id
- note_id
- source            note_syntax / ui / resurface / unchecked
- next_fire_at
- repeat_rule
- last_acknowledged_at
- status            active / due / overdue / dismissed
- review_stage
- last_reviewed_at
- next_review_at
- timezone
```

提醒的绝对时间统一以 UTC 存储，展示时使用客户端配置的时区。`status` 和相对时间是客户端根据当前时间临时计算的派生状态，不代表服务端已触发提醒。`last_acknowledged_at` 用于避免重复展示同一次提醒；状态变化不产生链上交易。

提醒完全由客户端处理，不使用服务端调度、推送或轮询：

- 客户端启动时检查一次。
- 完成区块同步后检查一次。
- 打开首页、日历页或提醒页时重新计算一次。
- 用户手动刷新时重新计算一次。
- 界面显示 `due`、`overdue`、`in 2h` 等相对状态。

提醒时间包含在加密 payload 中，服务端无法读取，也不需要知道 `next_fire_at`。客户端关闭期间不会产生实时提醒通知。

重复提醒在用户确认后推进到下一次计划时间；如果客户端长时间未启动，确认时直接跳到当前时间之后的下一次发生时间，不补发历史通知。客户端关闭期间不会产生实时提醒通知。

### 8.1 艾宾浩斯回顾

每天默认展示 5 条回顾笔记。所有笔记都可以进入候选池，不设置永久排除规则；已回顾过的笔记仍然可以再次出现，但到达回顾时间或已经逾期的笔记具有更高权重。

默认回顾间隔为：

```text
1 天 → 2 天 → 4 天 → 7 天 → 15 天 → 30 天 → 60 天
```

回顾状态是每个终端独立的本地状态，不在多个终端之间同步。客户端在笔记被用户确认已回顾后推进 `review_stage`，计算下一次 `next_review_at`；如果候选笔记少于 5 条，则展示全部候选笔记。当天的边界使用客户端配置时区。

## 9. 数据与存储架构

采用 Local-first 架构：本地端是交互入口，服务端是区块链账本节点和同步中继。链上追加日志是服务端的权威数据源，本地 SQLite 是可重建的明文缓存和搜索索引。

```text
Bubble Tea TUI
      │
      ▼
本地领域服务
      │
      ├── SQLite：笔记明文缓存、搜索索引、密钥引用、同步状态
      ├── 本地全文搜索
      └── Outbox：待进行 PoW 和上传的交易
              │
              ▼
      HTTP API + WebSocket
              │
              ▼
服务端领域服务
      │
      ├── 区块链账本：区块、交易、MMR Root
      ├── 授权公钥注册表
      └── 链状态索引
```

SnapNotes 服务端以 `snapnotes-server` 单一二进制发布。MVP 是每条时间流一个可信的单节点排序器：节点验证交易、按接收顺序打包区块并维护 MMR，不启用区块级 PoW 和分叉选择。后续多节点模式再启用区块级 PoW、节点认证、分叉选择和累计工作量；不能把 MVP 的单节点保证描述成去中心化共识。

### 9.1 本地核心表

```text
notes
- note_id
- transaction_id
- stream_id
- stream_sequence
- encrypted_payload
- payload_nonce
- key_epoch
- wrapped_dek
- plaintext_content
- author_public_key
- client_created_at
- included_at
- chain_status       pending / confirmed / orphaned

note_index
- note_id
- plaintext_content
- note_date
- has_unchecked
- search_vector

note_tags
- note_id
- tag

local_reminders
- reminder_id
- note_id
- source
- next_fire_at
- repeat_rule
- last_acknowledged_at
- status              derived, not persisted as authority
- review_stage
- last_reviewed_at
- next_review_at

outbox
- operation_id
- entity_id
- operation_type
- payload
- pow_nonce
- created_at
- sync_status

sync_state
- chain_id
- last_block_height
- last_block_hash
- last_mmr_root
- last_stream_sequence
- last_chainwork
- device_id
```

`note_index` 和 `notes.plaintext_content` 只存在于授权终端本地。服务端链上只保存加密交易，链上区块是唯一权威数据源。

本地 SQLite 明文缓存属于设备敏感数据：MVP 使用用户数据目录权限和操作系统密钥环保护本地密钥，不把密钥写入日志或普通配置文件。SQLCipher 或全盘加密属于后续增强，但备份和威胁模型必须明确告知用户：E2E 加密不能防止已解锁终端上的明文被读取。

### 9.2 服务端账本结构

```text
streams
- stream_id
- name
- owner_public_key
- genesis_block_hash
- pow_target
- pow_epoch
- created_at

blocks
- chain_id
- stream_id
- height
- previous_block_hash
- mmr_root
- timestamp
- pow_target
- pow_epoch
- block_work
- chainwork
- nonce
- block_hash

transactions
- transaction_id
- protocol_version
- stream_id
- block_height
- block_index
- note_id
- operation_type       create / key_grant / key_rotate / member_add / member_revoke
- encrypted_payload
- payload_nonce
- key_epoch
- wrapped_dek
- encrypted_stream_key
- recipient_encryption_public_key
- recipient_device_id
- author_public_key
- signature
- pow_nonce
- transaction_hash

authorized_keys
- public_key
- encryption_public_key
- device_id
- label
- status               active / revoked
- created_at
- effective_height
- revoked_height
- revoked_at

chain_peers
- peer_id
- endpoint
- public_key
- last_synced_height
- status

pow_epochs
- stream_id
- pow_epoch
- start_stream_sequence
- target
- created_in_block

```

`blocks` 和 `transactions` 组成每条时间流的链上数据；`streams`、`authorized_keys` 和 `pow_epochs` 是从创世配置及成员交易构建的状态投影，服务端不能绕过 Owner 交易直接修改它们。本地笔记、标签、搜索和提醒索引都可以从解密后的交易重新构建，不作为账本真源。服务端不保存客户端提醒索引。

服务端必须对 `transactions.transaction_id` 建立唯一约束；`(stream_id, block_height, block_index)` 也必须唯一。链重组时，旧主链交易保留为 `orphaned`，不能重新占用已确认链的位置。

## 10. 同步协议

### 10.1 本地写入

用户按下 `Enter` 后，在同一个本地事务中完成：

1. 创建一条不可变笔记。
2. 解析标签、日期和提醒。
3. 更新本地搜索索引。
4. 生成 `note_id`、`transaction_id` 和待签名交易并写入 `outbox`。
5. 更新 TUI 时间线。

任何网络请求都不能阻塞这条流程。

### 10.2 工作量证明与上传

客户端向服务端提交一次性的加密追加交易、签名和工作量证明。工作量证明提高批量垃圾写入的成本；`transaction_id` 负责同一交易的幂等去重。由于本项目没有 UTXO 或可转移资产，这里解决的是重复提交和 exactly-once 追加，不是 Bitcoin 意义上的双花。

交易 hash 需要满足当前难度目标：

```text
SHA256("snapnotes/pow/v1" || stream_id || transaction_id || author_public_key || pow_epoch || pow_nonce) < target
```

`target` 由创世区块和 `pow_epochs` 发布，客户端不能自行降低难度。PoW 哈希按 32 字节大端无符号整数比较；目标为 0 或超过最大 256 位整数时拒绝。初始目标由参考设备在 100ms 内的实测哈希率计算：`target = floor(2^256 / (hashrate * 0.1s)) - 1`，并写入创世区块。每 1000 条有效交易建立一个新 epoch，目标窗口为 100 秒，按前一 epoch 的实际主链时间跨度调整：`new_target = clamp(old_target * actual_window / 100s, old_target/4, old_target*4)`。只接受当前 `pow_epoch = floor(accepted_transaction_count / 1000)` 的交易，不接受上一 epoch 的宽限交易；接受第 1000 条交易的区块发布下一 epoch 的目标，供第 1001 条交易使用。

100ms PoW 只作为防垃圾写入的一层，不能单独抵抗并行刷写；服务端仍必须执行公钥授权、交易唯一 ID、单终端频率限制、单笔大小限制和区块大小限制。

服务端验证以下内容后接受交易：

- 作者公钥处于 `authorized_keys` 且未撤销。
- 签名与交易内容匹配。
- `transaction_id` 尚未处理。
- `pow_epoch` 与当前流状态匹配。
- 工作量证明满足当前难度。
- 交易类型和授权状态合法；普通笔记交易只能执行一次创建。

服务端将有效交易打包进下一个区块，并返回区块高度、区块 hash、`stream_sequence` 和 MMR Root。重复提交已接受的 `transaction_id` 时返回原始确认结果，不创建新笔记。签名、权限、大小或格式错误属于永久失败，不自动重试；过期 PoW epoch 属于可重试失败，客户端重新计算后再提交。

### 10.3 拉取

客户端按 `stream_id` 携带上次保存的区块高度和区块 hash 拉取变更。HTTP JSON 中所有二进制字段使用无填充 base64url，时间使用 RFC3339；服务端必须分页并限制响应大小：headers 单次最多 2000 个，blocks 单次最多 8 MiB 或 100 个区块。

```text
GET  /api/v1/streams/{stream_id}/tip
GET  /api/v1/streams/{stream_id}/headers?from_height=123&limit=2000
GET  /api/v1/streams/{stream_id}/blocks?from_height=123&limit=100
POST /api/v1/streams/{stream_id}/transactions
GET  /api/v1/streams/{stream_id}/proofs/{transaction_id}
WS   /api/v1/streams/{stream_id}/events
```

`tip` 返回 `height`、`block_hash`、`chainwork`、`mmr_root` 和 `leaf_count`。`headers` 只返回连续区块头；客户端验证后再通过 `blocks` 下载本体。`proofs` 返回交易所在区块、MMR inclusion proof 和对应 root。所有接口都必须设置请求超时、body 上限和明确错误码；服务端不得接受客户端提交的 height、sequence 或 root 作为权威状态。

客户端必须验证 `previous_block_hash`、交易签名和 MMR Root。多节点模式启用区块级 PoW 后，客户端还必须验证区块 PoW。WebSocket 只发送“有新区块”的通知，客户端收到通知后仍然通过区块高度补拉完整数据，避免断线导致数据缺失。

### 10.4 区块分叉

单节点模式中，节点负责验证交易和打包区块，链的最终性来自该节点的排序权。多节点模式启用区块级 PoW 和链选择规则：优先选择累计工作量最大的有效链；`chainwork` 为每个区块工作量 `floor(2^256/(target+1))` 的大整数累加值。节点不能仅根据区块高度选择主链。

终端只负责创建和计算交易 PoW，不直接决定主链。节点负责将交易排序进区块，并在区块内按 `transaction_id` 字典序排序，以便同一候选区块的序列化结果确定。多个终端可以并发写入，但最终顺序以主链区块顺序为准。

### 10.5 确认与回滚

链状态需要区分：

- `pending`：已收到但尚未达到确认条件。
- `confirmed`：当前主链上的稳定消息。
- `orphaned`：曾经属于某条分叉链，后来被主链选择规则回滚。

单服务端模式下，服务端追加到持久化区块文件并完成索引提交后可以立即标记为 `confirmed`。多节点 PoW 模式下，消息需要等待配置的确认数；被回滚的消息不从本地数据库删除，而是标记为 `orphaned`，重新进入 outbox 前必须生成新的明确交易。

### 10.6 并发追加

所有终端只向时间流追加新笔记，不修改已有笔记，因此不同终端同时写入不会产生编辑冲突，也不需要 CRDT。交易进入主链后，服务端按“前序区块交易数 + 当前区块内交易索引”分配 `stream_sequence`；该值是派生位置，不属于客户端签名交易正文。

笔记没有修正和删除交易。内容需要改变时创建新笔记；服务端只需要处理交易重复、授权状态和区块排序。

## 11. 端到端加密与写入权限

### 11.1 密钥角色

每个授权终端拥有两类密钥：

- 签名密钥：Ed25519 公钥登记在服务端，私钥用于签名交易。
- 加密密钥：X25519 公钥用于加密笔记密钥，私钥用于解密。

签名公钥和加密公钥不能混为一谈。服务端只保存公钥和加密后的数据，私钥永远不上传。

### 11.2 笔记加密流程

协议 v1 固定使用：Ed25519 签名、X25519 密钥协商、HKDF-SHA256 密钥派生、XChaCha20-Poly1305 AEAD 和 SHA-256 哈希。不得在同一协议版本中替换为 AES-GCM 或 BLAKE3；算法变化必须增加新的协议版本。

每条笔记使用两级信封加密：

1. 客户端使用 CSPRNG 生成 32 字节随机 `DEK`。
2. 使用 `DEK` 和 24 字节随机 nonce 加密正文、标签、用户日期、提醒和勾选项，形成 `encrypted_payload`。
3. 使用当前 `key_epoch` 的 32 字节 Stream Key 和独立 nonce 加密 `DEK`，形成 `wrapped_dek`。
4. 两次 AEAD 都使用 AAD 绑定 `protocol_version`、`stream_id`、`note_id`、`transaction_id`、`key_epoch` 和字段类型，防止密文被跨流或跨笔记搬运。
5. 客户端对规范化的交易正文生成 Ed25519 签名，再计算交易级 PoW。

`encrypted_payload` 和 `wrapped_dek` 的 nonce 必须分别随机生成且不得复用；nonce 与对应密文一起传输。服务端只保存密文、nonce、密钥 epoch 和密钥信封，不掌握任何私钥或 Stream Key 明文。

### 11.3 规范化交易格式

协议 v1 的哈希和签名输入使用 RFC 8949 deterministic CBOR。所有字段名、字段类型、整数宽度、字节序和可选字段规则固定在协议版本中；二进制内容使用 CBOR byte string，时间使用 UTC Unix milliseconds，枚举使用小写 ASCII 字符串。

交易分为三个输入：

```text
unsigned_body = { protocol_version, stream_id, note_id, operation_type,
                  operation_payload, client_created_at, author_public_key }
transaction_id = SHA256("snapnotes/txid/v1" || canonical(unsigned_body))
signature      = Ed25519.Sign(author_private_key,
                  "snapnotes/sign/v1" || canonical(unsigned_body))
pow_preimage   = "snapnotes/pow/v1" || stream_id || transaction_id ||
                  author_public_key || pow_epoch || pow_nonce
transaction_hash = SHA256("snapnotes/tx/v1" || canonical({
                  unsigned_body, transaction_id, signature, pow_epoch, pow_nonce
                }))
```

`transaction_id` 不包含签名和 `pow_nonce`，因此同一笔交易重新计算 PoW 或重传时仍然幂等；不同内容必须产生不同的 `transaction_id`。`transaction_hash` 不参与 `transaction_id` 计算，避免自引用。签名不覆盖 PoW nonce，但服务端验证 nonce、epoch 和 transaction_id 的组合；改变 nonce 不能改变交易身份或绕过签名。

交易使用 `transaction_hash` 作为 MMR 叶子输入。服务端拒绝未知字段、重复字段、错误 CBOR 编码和超过大小上限的交易。

### 11.4 多终端密钥分发与轮换

创世配置创建 `key_epoch=0` 的 Stream Key。每个终端拥有独立的 Ed25519 签名密钥和 X25519 加密密钥，签名公钥、加密公钥、设备 ID 和标签由 Owner 交易登记。

新终端加入流程如下：

1. 新终端生成两类密钥，并对 `join_request` 签名。
2. 用户通过本地文件、二维码或复制粘贴把 join request 交给 Owner 终端；该步骤是带外信任确认，不依赖服务端自动批准。
3. Owner 校验新终端签名后生成 `member_add` 交易。
4. Owner 为新终端生成 `key_grant` 交易，使用新终端的 X25519 公钥分别加密需要授予的历史和当前 Stream Key。
5. 服务端只接受 Owner 对 `member_add`、`member_revoke`、`key_grant` 和 `key_rotate` 的签名。

撤销使用 Owner 签名的原子 `key_rotation_bundle` 交易，而不是独立接受的 `member_revoke` 和 `key_rotate` 交易。该交易同时包含被撤销签名公钥、新的 Stream Key epoch，以及每个仍 active 终端的 `key_grant`。服务端必须整体验证和写入该 bundle，不能在其中插入普通笔记交易；成功后立即拒绝被撤销密钥和旧 epoch 的后续写入。被撤销终端可以继续读取它已经取得的历史 epoch，但不能获得新 epoch。历史密文和历史密钥不会被远程收回。

第一版默认所有 active 终端都可以读取完整共享时间流。Stream Owner 的签名密钥在创世配置中确定，服务端不能直接修改成员列表。未来如果需要不同终端只能读取部分消息，再增加按笔记或按标签的 recipient policy。

### 11.5 私钥备份

私钥只保存在用户控制的终端或用户自行管理的加密备份中。服务端不保存私钥、明文密钥包或恢复口令，也不承担密钥找回责任。

### 11.6 写入规则

- 持有已登记签名公钥对应私钥的 active 终端可以向同一个共享时间流追加新笔记；仅有公钥不能写入。
- 每条交易必须包含作者公钥和签名。
- 每条普通笔记只能创建一次，不允许原地编辑或删除。
- 内容需要修正时，重新创建一条独立笔记。
- Stream Owner 可以撤销某个公钥；撤销不影响该公钥之前已经写入的区块。

### 11.7 传输安全

笔记内容使用端到端加密。客户端与服务端之间仍使用 TLS，保护密文交易和密钥信封在网络中的传输过程。

## 12. 搜索与索引

由于笔记正文是端到端加密数据：

- 全文搜索只能在授权终端解密后执行。
- TUI 使用 SQLite FTS5 建立本地索引，保证离线查询速度。
- 新终端首次同步后，先下载并验证区块，再解密交易并重建索引。
- 标签、日期、提醒和勾选项都包含在加密 payload 中。
- 服务端只提供按区块高度、交易 ID 和同步状态的查询。
- 链上交易仍然是本地索引重建的唯一来源。

## 13. 区块链账本与 MMR 结构

笔记变更采用追加式交易和区块，而不是只保存可覆盖的最终状态。

交易包含：

```text
transaction
- protocol_version
- transaction_id
- stream_id
- note_id
- operation_type
- operation_payload
- client_created_at
- author_public_key
- signature
- pow_epoch
- pow_nonce
- transaction_hash
```

区块包含：

```text
block
- chain_id
- stream_id
- height
- previous_block_hash
- mmr_root
- pow_target
- pow_epoch
- block_work
- chainwork
- timestamp
- nonce
- transactions
- block_hash
```

### 13.1 MMR 决策

SnapNotes 选择 Merkle Mountain Range（MMR）作为共享时间流的主完整性结构，不使用普通 Merkle Tree 作为全局账本结构。

原因：

- 时间流是持续追加的，不需要原地更新。
- MMR 可以增量追加，避免每次重建整棵树。
- MMR 支持消息包含证明和追加一致性证明。
- MMR 的 Root 可以直接写入区块头。

MMR 叶子使用已接受交易的 `transaction_hash`：

```text
leaf_hash = SHA256("snapnotes/mmr-leaf/v1" || transaction_hash)
```

同一区块内交易按 `transaction_id` 字典序排列，区块之间按 height 排列；MMR 只在交易成功进入当前主链后追加。`stream_sequence` 等于该交易在主链上的零基叶子序号。MMR 的峰值按从低位到高位固定顺序折叠生成 `mmr_root`，使用 RFC 6962 风格的左右域分离 hash；proof 编码使用 deterministic CBOR。任何实现都不能依赖语言默认序列化。

区块头中的 `mmr_root` 是该区块所有交易追加完成后的流状态 root。链重组时，节点从分叉点保存的 MMR 快照或 undo 日志恢复峰值，再按新主链重新追加；不能直接沿用旧主链的 root。MVP 单节点没有分叉，但数据格式必须保留该恢复能力。

### 13.2 MMR 状态

服务端需要持久化最小 MMR 状态：

```text
mmr_state
- stream_id
- leaf_count
- peaks
- current_root
- snapshot_height
- snapshot_hash
```

MMR 节点可以保存在 Pebble/LevelDB 或追加式文件中。SQLite 不作为 MMR 的核心存储；本地 SQLite 只保存同步状态、解密缓存和搜索索引。

### 13.3 完整性与共识边界

MMR 负责：

- 证明消息属于某条链。
- 检测交易内容被修改。
- 提供消息包含证明。
- 提供追加一致性证明。

MMR 不负责 PoW 共识、分叉选择、加密、搜索或提醒调度。区块级 PoW 和累计链工作量只在多节点模式下决定主链。

需要解决的问题对应如下：

| 问题 | 机制 |
| --- | --- |
| 重复提交 | `transaction_id` 唯一约束和幂等处理，PoW nonce 不参与 transaction_id |
| 重放攻击 | `transaction_id` 唯一约束、签名、流 ID 和区块确认 |
| 垃圾写入 | 授权公钥检查和交易级 PoW |
| 同笔记并发编辑 | 不支持原地编辑，重新创建独立笔记 |
| 历史篡改检测 | 交易 hash、区块 hash 和 MMR Root |
| 并发写入排序 | 区块高度、区块内交易顺序 |
| 服务端重写历史 | 终端保存区块 hash，未来可外部锚定 Root |

工作量证明用于提高伪造或批量写入的成本，不等于权限控制，也不能单独解决重复提交。由于本项目没有可转移资产，不存在传统 UTXO 双花；同一笔笔记的 exactly-once 追加由 `transaction_id` 幂等约束解决。MMR 计算的是加密交易的 hash，不需要暴露明文。单节点链只能证明“从已知链头开始未被修改”，不能抵抗恶意节点回滚或重写；多节点共识或外部 Root 锚定才提供更强的历史抗回滚能力。

单节点 MVP 中，节点负责打包区块，终端负责交易级 PoW。未来扩展多个链节点时，再增加节点身份握手、交易/区块广播、区块级 PoW、分叉选择、累计工作量和链重组测试。

## 14. 技术建议

- 语言：Go。
- TUI：Bubble Tea v2。
- 本地数据库：SQLite。
- 本地全文搜索：SQLite FTS5。
- 服务端区块本体：追加式区块分段文件，例如 `blocks/blk-*.dat`。
- 服务端区块索引：嵌入式 KV 存储，具体实现可选 Pebble、LevelDB 或其他经过验证的实现。
- 服务端链状态：独立保存当前主链、累计工作量、成员和密钥 epoch，不与区块本体混存。
- 服务端 MMR 状态：保存 `leaf_count`、peaks 和当前 `mmr_root`。
- 服务端派生索引：可选 SQLite/PostgreSQL，但不能替代区块链账本，也不建立正文搜索索引。
- API：HTTP JSON。
- 实时通知：WebSocket。
- 传输加密：TLS。
- 交易签名：Ed25519。
- 密钥封装：X25519 或成熟的多接收者加密方案。
- 哈希：SHA-256。
- 工作量证明：MVP 使用可配置难度的交易级 PoW 作为防垃圾机制；多节点模式增加区块头 PoW。
- PoW 目标：基准设备上单笔交易预计约 100ms 完成，默认每 1000 条有效交易调整一次，不保证所有硬件严格耗时 100ms。
- 时间规则：内部统一 UTC，界面使用用户时区。

### 14.1 存储选择结论

SQLite 适合本地端，因为它提供事务、全文搜索、离线能力和零配置部署。SQLite 也可以用于小规模服务端派生索引，但不作为区块本体或 MMR 的权威存储。

服务端采用“区块分段文件 + KV 索引 + MMR 状态”的分层结构。这样可以将不可变的链数据、可重建的索引和当前链状态分开处理；索引损坏时可以从区块文件重建。

Bitcoin Core 的实现也采用类似分工：区块本体使用 `blocks/blk*.dat`，区块索引和 chainstate 使用 LevelDB，撤销数据使用 `blocks/rev*.dat`。SnapNotes 没有 UTXO，因此不需要复制 Bitcoin 的 chainstate 模型，只需要维护共享时间流的主链状态和本地搜索投影。

### 14.2 节点部署与全量同步

服务端只发布一个可执行二进制，不要求用户部署数据库或运行额外服务：

```text
snapnotes-server --data-dir ./snapnotes-data --listen 0.0.0.0:8333
snapnotes-server --data-dir ./snapnotes-data --listen 0.0.0.0:8333 --peer node.example.invalid:8333
```

节点启动后可以从任意 Peer 获取完整数据：

1. 获取并验证创世区块和区块头。
2. 以 headers-first 方式下载区块头。
3. 根据当前主链和累计工作量下载完整区块。
4. 验证区块 hash、交易签名、MMR Root 和 PoW（多节点模式）。
5. 将区块写入本地区块文件，并重建 KV 索引和 MMR 状态。

TUI 客户端可以连接任意可用节点。节点之间同步的是加密交易和区块数据，Peer 不需要拥有 Stream 私钥，也不能读取笔记正文。

建议保持单仓库，入口分为：

```text
cmd/snapnotes/
cmd/snapnotes-server/
internal/domain/
internal/parser/
internal/search/
internal/reminder/
internal/sync/
internal/storage/sqlite/
internal/storage/blocks/
internal/storage/kv/
internal/crypto/
internal/api/
```

## 15. MVP 范围

### Phase 1：本地笔记

- Bubble Tea 首页。
- 最近笔记流。
- 底部输入框。
- `Enter` 保存。
- 多行输入模式。
- SQLite 存储。
- 标签和轻量语法解析。
- 本地全文搜索。

### Phase 2：本地提醒

- 指定时间提醒。
- 重复提醒。
- 日历页。
- 每天 5 条的艾宾浩斯间隔回顾。
- 未勾选项回顾。

### Phase 3：端到端加密的单节点同步

- `snapnotes-server` 单二进制节点。
- 终端签名和加密密钥生成与授权公钥注册。
- 每条笔记的 DEK、Stream Key 和单终端密钥信封。
- 交易级 PoW。
- 区块上传和区块高度拉取。
- Peer headers-first 全量同步。
- WebSocket 实时通知。
- 断线补拉、链校验和重复交易处理。

### Phase 4：多终端授权与密钥轮换

- 共享时间流密钥 epoch 和终端撤销。
- 多个授权终端同时追加笔记。
- 作者公钥和签名展示。
- 公钥撤销。

### Phase 5：链完整性

- 区块级 PoW。
- 多节点账本同步。
- 分叉选择和累计工作量规则。
- MMR Proof。
- 本地链校验和审计信息展示。

## 16. 验收标准

- 断网时创建笔记不会失败，也不会阻塞输入框。
- 重启应用后，未同步笔记仍然存在并会自动重试。
- `Enter` 可以创建新笔记，多行模式不会拆分笔记。
- 可以按正文、标签、创建时间和提醒状态搜索。
- 只有授权公钥对应的签名终端可以写入，笔记正文在服务端保持密文。
- 同一操作重复上传不会产生重复笔记。
- 两个设备同时创建笔记时，两条笔记都能最终出现在本地。
- 每条笔记只写入一次，不支持原地编辑和删除。
- 服务端变化可以通过区块高度和区块 hash 补拉，WebSocket 断线不会造成数据缺失。
- MMR 校验能够发现交易内容被修改，并验证消息包含证明。
- 私钥可以由用户导出为加密备份并在另一终端恢复，服务端不会保存私钥。
- `snapnotes-server` 可以从任意 Peer 获取并验证完整区块数据。
- 在约定的基准设备上，单笔交易 PoW 的目标耗时约为 100ms。

## 17. 已确认默认参数

以下参数已经确定：

1. 每天回顾 5 条笔记。
2. 使用艾宾浩斯式间隔安排回顾，不设置永久排除规则。
3. PoW 基准目标约为 100ms，每 1000 条有效交易调整一次难度。

## 18. 参考资料

- Bitcoin Developer Guide - Block Chain：<https://developer.bitcoin.org/devguide/block_chain.html>
- Bitcoin Core 区块链数据库实现：<https://github.com/bitcoin/bitcoin/blob/master/src/txdb.cpp>
- Bitcoin Core 区块存储与区块索引：<https://github.com/bitcoin/bitcoin/blob/master/src/node/blockstorage.cpp>

这些资料用于确认 Bitcoin 的区块头、前区块 hash、Merkle Root、区块 PoW、区块文件、区块索引和 chainstate 的职责边界。SnapNotes 采用 MMR，是因为它的共享时间流是持续追加日志，而不是 Bitcoin 的 UTXO 支付账本。
