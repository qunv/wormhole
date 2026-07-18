# Codebridge architecture

## 1. Mục tiêu thiết kế

Codebridge dùng một binary cho cả CLI supervisor và MCP server. CLI chỉ chịu trách nhiệm load config, quản lý process và tunnel; business logic của toàn bộ tools nằm trong `agent.Runtime`, nên foreground server, background server và tests cùng dùng một policy/state implementation.

Các mục tiêu chính:

1. Một MCP gateway duy nhất cho workspace, CodeGraph, Figma và memory.
2. Tool contract ổn định, không phụ thuộc backend cụ thể.
3. Path confinement và policy enforcement trước mọi mutation.
4. Secret tách khỏi persistent non-secret config.
5. Memory fail-open theo mặc định, không làm chậm coding workflow.
6. Có thể thêm provider hoặc migrate memory mà không đổi workflow của model.

## 2. Control flow tổng thể

```text
CLI command
   │
   ├── setup/config/key/tunnel
   │      ├── config.json (non-secret)
   │      └── .env (runtime secrets)
   │
   ├── start/stop/restart/status/doctor
   │      ├── process state
   │      ├── config ID
   │      └── health checks
   │
   └── serve
          │
          ▼
HTTP server
   ├── body limit
   ├── bearer auth
   ├── Origin/CORS guard
   └── health endpoints
          │
          ▼
Official MCP Go SDK
   ├── Streamable HTTP
   ├── logical ServerSession
   ├── embedded Apps resource
   └── 87 registered tools
          │
          ▼
agent.Runtime.HandleSession
   ├── normalize args
   ├── enforce policy / consume exact approval
   ├── dispatch group handler
   ├── append redacted audit record
   └── enqueue redacted memory observation
```

`Runtime.Handle` vẫn tồn tại cho internal callers và tests; nó dùng fallback session identity. MCP requests đi qua `HandleSession` để giữ logical connection identity.

## 3. Package boundaries

| Package | Trách nhiệm |
|---|---|
| `cmd/codebridge` | Process entrypoint và exit code |
| `internal/app` | Version/tier metadata và composition root |
| `internal/cli` | CLI grammar, setup, process lifecycle, tunnel install/profile |
| `internal/server` | HTTP routing, health, auth, Origin/CORS và body limits |
| `internal/mcpserver` | MCP server construction, session identity, widget và result adapter |
| `internal/agent` | 87-tool registry, shared runtime, policy và tool handlers |
| `internal/workspace` | Canonical paths, configured roots, owning-root, list/search/tree |
| `internal/security` | Shell/Git guards, risk classification, approvals và redaction |
| `internal/patch` | Backup batches, structured operations, unified diff và undo |
| `internal/processx` | Timeout, output caps và managed process trees |
| `internal/figma` | MCP client bridge đến Figma Desktop |
| `internal/memory` | Canonical contracts, project identity, async recorder và adapters |
| `internal/state` | Per-workspace notes, task, decisions, audit, index và backups |
| `internal/assets` | Embedded MCP Apps widget và built-in skills |

Dependency direction quan trọng:

```text
mcpserver → agent → memory.Provider
                    ↑
             provider adapters
```

`agent` không import `agentmemory`; adapter cụ thể chỉ được chọn qua `memory/factory`.

## 4. Configuration lifecycle

Config được hình thành theo thứ tự:

```text
Default()
  → JSON unmarshal từ config.json
  → environment overrides
  → normalize + validate
  → CLI options của lần chạy hiện tại
```

### Config locations

| Hệ điều hành | Config directory |
|---|---|
| Linux | `$XDG_CONFIG_HOME/codebridge`, fallback `~/.config/codebridge` |
| macOS | `~/Library/Application Support/Codebridge` |
| Windows | `%APPDATA%\Codebridge` |

Runtime state dùng app-data/state directory:

| Hệ điều hành | State directory |
|---|---|
| Linux | `$XDG_STATE_HOME/codebridge`, fallback `~/.local/state/codebridge` |
| macOS | `~/Library/Application Support/Codebridge` |
| Windows | `%LOCALAPPDATA%\Codebridge` |

### Secret ownership

```text
config.json
  workspace, mode, policy, tunnel metadata,
  Figma config, memory config, limits

.env
  CONTROL_PLANE_API_KEY
  optional memory provider secret
```

`Config.Save` xóa `AuthToken` và `ApprovalToken` trước khi serialize. Memory secret không nằm trong `MemoryConfig`; config chỉ lưu tên biến `secretEnv`.

Memory provider options được validate đệ quy. Key có tên chứa `secret`, `password`, `token`, `apikey`, `authorization` hoặc `credential` bị từ chối để tránh ghi credential vào JSON.

### Config identity và process reuse

Supervisor tạo `ConfigID` từ:

- workspace và extra roots;
- mode, policy, port và auth-enabled state;
- binary hash và widget hash;
- Figma endpoint;
- toàn bộ non-secret memory config;
- fingerprint rút gọn của memory secret.

Nếu health endpoint báo cùng `ConfigID`, supervisor tái sử dụng server. Đổi memory agent ID, retry config, provider options hoặc secret làm `ConfigID` thay đổi và buộc server mới được tạo.

## 5. HTTP và MCP layer

Codebridge expose:

```text
/mcp                 public MCP endpoint
/healthz             public health
/internal/healthz    supervisor health có PID và config ID
```

Non-loopback `host` bắt buộc có MCP bearer token. Browser Origin chỉ được phép nếu là loopback hoặc nằm trong explicit allowlist.

`agent.Tools()` là source of truth duy nhất cho:

- name;
- title và description;
- JSON input schema;
- read-only/destructive annotations;
- MCP Apps metadata.

`internal/mcpserver` chuyển tool result thành cả JSON text và `structuredContent` khi output là object. Errors được trả bằng `CallToolResult{IsError:true}`, không biến thành protocol failure.

## 6. Runtime pipeline và policy

Mỗi tool request đi theo pipeline:

```text
args normalization
  → enforcePolicy
  → dispatch
  → audit
  → captureMemoryObservation
```

Nếu policy từ chối request, runtime vẫn ghi audit failure nhưng không capture post-tool observation vì tool chưa chạy.

### Policies

- `strict`: chặn mutation tools.
- `balanced`: cho phép edit nhưng yêu cầu exact one-time approval cho action rủi ro.
- `full`: không yêu cầu approval thông thường, nhưng command guard vẫn chặn catastrophic operations.

Approval action chứa exact target hoặc exact arguments. `memory_forget` serialize arguments vào approval action, nên approval không thể được tái sử dụng cho memory khác.

## 7. Workspace identity và confinement

`workspace.Manager` giữ:

```text
Primary
Roots
realRoots
Skips
RGBin
```

`Resolve`:

1. Resolve relative path từ primary root.
2. Tìm longest existing ancestor.
3. Canonicalize ancestor qua symlink.
4. Ghép phần path chưa tồn tại trở lại.
5. Kiểm tra path canonical nằm trong một configured real root.

Nhờ vậy cả target chưa tồn tại cũng không thể escape qua symlink.

### Owning root

Một resolved path có thể nằm trong primary root hoặc extra root. `OwningRoot` chọn configured root cụ thể nhất chứa path đó. Project identity, đặc biệt `path-hash`, luôn được tính từ owning root chứ không từ subdirectory/file.

Ví dụ:

```text
configured root: /repo
request path:    /repo/internal/memory/provider.go
project root:    /repo
```

Điều này ngăn memory bị phân mảnh theo `cwd` hoặc thư mục con.

## 8. Memory architecture

### 8.1 Canonical contract

`memory.Provider` định nghĩa core operations:

```text
Health
Search
Context
Remember
Observe
Forget
Close
```

Optional interfaces:

```text
memory.Exporter
memory.Importer
```

Outputs không trả raw backend payload. Adapter normalize thành:

- `Item`;
- `SearchResult`;
- `ContextResult`;
- `RememberResult`;
- `ForgetResult`;
- `ExportResult`;
- `ImportResult`.

`ProviderID` giữ backend identifier, còn MCP clients chỉ phụ thuộc canonical fields.

### 8.2 Provider registry

`memory/factory` dùng registry:

```go
factory.Register("provider-name", constructor)
```

Built-in registrations:

```text
none
agentmemory
```

Khi memory tắt hoặc provider là `none`, factory trả no-op provider. Runtime và MCP tool registry không cần thay đổi khi thêm backend mới.

### 8.3 Agentmemory adapter

Adapter dùng REST endpoints mặc định:

```text
GET  /agentmemory/health
GET  /agentmemory/config/flags
POST /agentmemory/search
POST /agentmemory/context
POST /agentmemory/remember
POST /agentmemory/observe
POST /agentmemory/forget
GET  /agentmemory/export
```

Paths và response-size limit có thể override qua `memory.options`.

Bearer header chỉ được gửi khi biến được chỉ định bởi `secretEnv` có giá trị.

Health capabilities bắt đầu từ adapter support, sau đó được điều chỉnh bằng health/flags response nếu backend công bố feature state. Context kết hợp session context với query search; khi context endpoint không được hỗ trợ, adapter fallback sang narrative search.

### 8.4 Project identity

Hai strategy:

```text
git-origin
path-hash
```

`git-origin` đọc `remote.origin.url`, hỗ trợ HTTPS, SSH và SCP-like URL, loại credentials, xóa `.git` và lowercase:

```text
git@github.com:Owner/Repo.git
https://github.com/Owner/Repo.git
       ↓
git:github.com/owner/repo
```

Nếu Git origin không tồn tại hoặc không hợp lệ, resolver fallback sang:

```text
workspace:<sha256-prefix-of-owning-root>
```

`path-hash` luôn dùng fallback form này.

### 8.5 Session identity

MCP layer lấy identity từ `ServerSession`:

```text
mcp:<protocol-session-id>
```

Một số transports không gán protocol ID. Khi đó Codebridge hash process-local identity của stable session object:

```text
mcp-local:<hash>
```

Fallback process session chỉ dành cho internal callers không đi qua MCP. Vì vậy concurrent MCP connections được tách khỏi nhau.

### 8.6 Retrieval semantics

`memory_context` dùng:

- task query;
- project ID;
- owning-root cwd;
- agent ID;
- MCP session ID;
- token budget.

Adapter có thể dùng session context endpoint, search endpoint hoặc cả hai. Result vẫn theo canonical `ContextResult`.

Memory là historical evidence. MCP instructions yêu cầu model verify implementation details bằng CodeGraph hoặc current files trước khi edit.

### 8.7 Explicit writes

`memory_remember` lưu một durable memory có kind, concepts, files và optional TTL.

`memory_commit` có thể tự tổng hợp:

- summary do caller cung cấp;
- current task plan;
- checkpoint;
- Git change summary;
- review result;
- files touched;
- next steps.

Memory được gắn project, agent và MCP session identity.

### 8.8 Auto-capture

Capture modes:

```text
off
selected
metadata
```

`selected` chỉ capture nhóm tools có historical value: notes/checkpoints, plan/decision, file mutations, Git, tests/build/lint, quality gate, CodeGraph, review và session report.

`metadata` capture rộng hơn nhưng chỉ giữ các field như path, cwd, staged, recursive và kind.

Memory tools, `ping` và `proc_output` không được auto-capture để tránh recursion hoặc output leakage.

Before enqueue:

1. Input đi qua recursive redaction.
2. Git chỉ giữ operation và arg count, không giữ remote/auth arguments.
3. Result chỉ lấy whitelist fields.
4. Failure chỉ ghi generic message; raw error nằm trong local audit.

### 8.9 Recorder delivery guarantees

Recorder dùng bounded channel và non-blocking enqueue:

```text
queue available → enqueued
queue full      → dropped++, tool response không bị block
```

Worker delivery:

```text
per-attempt timeout
  → exponential retry
  → max attempts
  → delivered hoặc failed
```

Backoff cap là 2 giây. Khi shutdown:

1. `closed=true` chặn records mới.
2. Worker nhận stop.
3. Queue hiện tại được drain.
4. Provider đóng sau recorder.

Recorder stats:

```text
queue_depth
queue_capacity
enqueued
delivered
retried
failed
dropped
```

Đây là at-most-once enqueue với bounded retry delivery; không có durable local spool. Process crash vẫn có thể mất observations chưa gửi.

### 8.10 Health và fail-open

`memory.required=false`:

- runtime khởi động dù provider offline;
- memory tool calls có thể trả lỗi;
- coding tools tiếp tục hoạt động;
- async failures chỉ tăng recorder counters.

`memory.required=true`:

- startup gọi provider health;
- unavailable provider làm runtime creation fail.

Health được cache theo `healthCacheMs` để `workspace_snapshot` và `workspace_doctor` không gọi backend liên tục.

### 8.11 Migration boundary

`memory_export` trả canonical schema version 1 dưới dạng object hoặc JSONL.

Agentmemory export có thể trả nhiều project; adapter normalize rồi lọc theo requested project. `memory_import` replay từng canonical item qua provider `Remember` thay vì restore raw database dump.

Trade-off:

- portable giữa providers;
- không phụ thuộc database schema;
- provider-specific fields chỉ được giữ trong bounded metadata;
- import có thể không bảo toàn internal embeddings hoặc graph IDs.

## 9. Local state ownership

Per-workspace state nằm dưới:

```text
workspaces/<workspace-id>/
  notes.json
  checkpoint.json
  current-task.json
  decisions.md
  audit.jsonl
  index.json
  patch-history.json
  backups/
  approvals/
```

Workspace ID dùng canonical primary workspace path. Memory provider data không được lưu trong local state directory, ngoại trừ audit/counters liên quan tool calls.

## 10. Security invariants

1. Mọi file path được resolve trong configured roots.
2. Longest existing ancestor được canonicalize trước containment check.
3. Configured root không thể bị delete/rename qua dedicated tools hoặc patch operations.
4. Command `cwd` bị root-confined, nhưng command execution không phải OS sandbox.
5. Raw Git chặn flags có thể ghi arbitrary output, đổi worktree hoặc chạy external program.
6. Balanced policy dùng exact expiring one-time approvals.
7. Audit args được redact đệ quy.
8. Auto-memory capture không gửi raw source, patch, command, stdout hoặc raw errors.
9. Provider options không được chứa secret-like keys.
10. Browser Origin bị chặn mặc định ngoài loopback/allowlist.
11. Non-loopback MCP listener bắt buộc có bearer token.
12. HTTP response từ memory provider bị giới hạn kích thước trước decode.

## 11. Testing strategy

Contract tests khóa:

- tool count và unique dispatch group;
- MCP in-memory round trip;
- session ID propagation;
- path traversal/root deletion rejection;
- secret separation và `.env` permission;
- ConfigID sensitivity;
- provider REST mapping và response normalization;
- no-secret auth behavior;
- context fallback;
- project normalization và owning-root behavior;
- recorder delivery, retry, drain và dropped counters;
- required/optional provider startup behavior;
- export filtering và canonical import.

Recommended verification:

```bash
go test ./...
go test -race ./internal/memory/... ./internal/agent ./internal/cli ./internal/config ./internal/mcpserver
go vet ./...
go build ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=arm64 go build ./...
```

## 12. Release

`.goreleaser.yml` build binaries cho:

- Linux amd64/arm64;
- macOS amd64/arm64;
- Windows amd64/arm64.

Version được inject qua:

```text
-X codebridge/internal/app.Version=<version>
```

MCP tool contract là public compatibility surface. Thay provider adapter không nên đổi tool names hoặc canonical result fields; breaking contract changes cần versioning/migration rõ ràng.
