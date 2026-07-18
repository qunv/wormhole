# Codebridge architecture

## Mục tiêu

Codebridge dùng một binary cho cả CLI supervisor và MCP server. CLI không chứa business logic của tools; mọi tool đi qua `agent.Runtime`, vì vậy server foreground, background lifecycle và tests dùng cùng policy/state implementation.

```text
CLI command
   │
   ├── setup/config/key/tunnel ── local config + secret env
   ├── start/stop/status ───────── process state + health checks
   └── serve
        │
        ▼
HTTP guard ── origin / bearer / body limit
        │
        ▼
Official MCP Go SDK (stateless Streamable HTTP)
        │
        ▼
agent.Runtime.Handle
   ├── policy + exact approval
   ├── audit redaction
   ├── workspace confinement
   ├── provider-neutral memory + async recorder
   └── tool group handler
```

## Package boundaries

| Package | Trách nhiệm |
|---|---|
| `cmd/codebridge` | Process entrypoint và exit code |
| `internal/app` | Version/tier và composition root |
| `internal/cli` | CLI grammar, setup, daemon lifecycle, tunnel install/profile |
| `internal/server` | HTTP routing, health, auth, origin/CORS, limits |
| `internal/mcpserver` | MCP server construction, widget resource, result adapter |
| `internal/agent` | 87-tool registry, shared runtime, policy dispatch |
| `internal/workspace` | Canonical paths, roots, list/search/tree |
| `internal/security` | Shell/git guard, risk classification, approval, audit redaction |
| `internal/patch` | Backup batches, operations, unified diff, undo |
| `internal/processx` | Timeout, output cap, background process tree |
| `internal/figma` | MCP client bridge đến Figma Desktop |
| `internal/memory` | Provider contract, project scoping, async recorder và backend adapters |
| `internal/state` | State tách theo workspace hash |
| `internal/assets` | Embedded HTML widget và skills |

## State ownership

Config có path theo OS:

- Linux: `$XDG_CONFIG_HOME/codebridge/config.json`
- macOS: `~/Library/Application Support/Codebridge/config.json`
- Windows: `%APPDATA%\Codebridge\config.json`

Runtime state nằm dưới app data directory và được chia theo hash của canonical workspace:

```text
workspaces/<workspace-id>/
  notes.json
  checkpoint.json
  current-task.json
  decisions.md
  index.json
  patch-history.json
  backups/
  approvals/
```

Secret không được serialize vào config. Runtime API key và optional memory secret nằm trong file `.env` permission `0600` hoặc environment; toàn bộ memory config không nhạy cảm nằm trong `config.json`.

## Memory provider boundary

ChatGPT chỉ thấy contract `memory_*` của Codebridge. `memory.Provider` map contract này sang backend cụ thể; adapter đầu tiên dùng agentmemory REST. Search/context/remember/forget outputs được normalize thành schema Codebridge thay vì trả raw backend data. Project scope ưu tiên normalized Git remote và fallback sang hash của configured owning root. MCP `ServerSession.ID()` tách observations theo kết nối. Recorder dùng bounded queue, retry/backoff, drain khi shutdown, fail-open và redaction trước khi gửi observation; provider offline không chặn code tools trừ khi `memory.required=true`.

Provider factory dùng registry constructor. Optional `memory.Exporter` và `memory.Importer` tạo canonical object/JSONL migration boundary; agentmemory export được normalize, còn import replay qua provider `Remember` để không phụ thuộc raw dump format.

## MCP contract

`agent.Tools()` là source of truth duy nhất cho name, title, description, annotation, schema và Apps metadata. Test khóa số lượng ở 87, kiểm tra unique names và gọi round-trip bằng MCP in-memory transport.

Low-level `Server.AddTool` được dùng để:

- giữ JSON schemas gần contract Node hiện tại;
- tự kiểm soát mapping tool error thành `CallToolResult{isError:true}`;
- passthrough nguyên `CallToolResult` từ Figma upstream;
- trả cả text JSON và `structuredContent` khi output là object.

## Security invariants

1. Mọi path được resolve từ primary root hoặc absolute path trong roots.
2. Longest existing ancestor được canonicalize trước khi kiểm tra containment, nên target chưa tồn tại vẫn không thể đi qua symlink.
3. Root không thể bị delete/rename qua dedicated tools hoặc patch operations.
4. Raw git chặn flags có thể đổi worktree, ghi arbitrary output hoặc chạy external program.
5. Background processes được đặt trong process group; stop cố gắng terminate cả tree.
6. Audit arguments được redact đệ quy; content, command, diff, token và secret fields không được ghi nguyên văn.
7. Browser Origin bị chặn mặc định ngoài loopback và explicit allowlist.

## Release

`.goreleaser.yml` build static binaries cho:

- Linux amd64/arm64
- macOS amd64/arm64
- Windows amd64/arm64

Version được inject qua:

```text
-X codebridge/internal/app.Version=<version>
```
