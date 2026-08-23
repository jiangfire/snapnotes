# SnapNotes

本地优先的 Go/Bubble Tea 笔记 TUI。当前已支持本地 SQLite 持久化、轻量语法解析、全文搜索、提醒状态和每日回顾领域逻辑。

## 开发命令

```powershell
$env:GOTELEMETRY='off'; go test ./...
$env:GOTELEMETRY='off'; go vet ./...
go build ./cmd/snapnotes
```

当前环境的 Go telemetry 目录不可写，因此测试命令显式关闭 telemetry。

## 运行

```powershell
go run ./cmd/snapnotes
```

首页按 Enter 保存笔记，Ctrl/Alt+Enter 插入换行；使用 ↑/↓ 选择笔记，Ctrl+R 确认提醒。
