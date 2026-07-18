# Codebridge

Codebridge là local coding agent viết bằng Go, đóng gói trong một binary duy nhất. Binary này quản lý workspace, chạy MCP server local, nối ChatGPT Web qua Secure MCP Tunnel, bridge Figma Desktop MCP, tích hợp CodeGraph và cung cấp **87 MCP tools** cho đọc/sửa code, chạy lệnh, Git, planning, review, approval và project memory.

## Điểm chính

- Native Go CLI cho `setup`, `start`, `stop`, `restart`, `status`, `doctor`, `workspace`, `logs`, `config`, `key`, `skills`, `figma` và `tunnel`.
- Stateless Streamable HTTP MCP tại `/mcp`; public health tại `/healthz` và supervisor health tại `/internal/healthz`.
- 87 tools được đăng ký từ một contract duy nhất trong `agent.Tools()`.
- Root confinement chặn path traversal và symlink escape.
- Policy `strict`, `balanced`, `full`; exact-action approval dùng một lần cho hành động rủi ro.
- Embedded MCP Apps widget, không cần web bundle riêng.
- Optional CodeGraph navigation và Figma Desktop MCP bridge.
- Provider-neutral project memory với agentmemory adapter, async capture, retry/backoff và export/import canonical.
- Build cho Linux, macOS và Windows.

## Yêu cầu

- Go 1.25 trở lên.
- Git để nhận repository root và project identity.
- `rg` là tùy chọn; nếu thiếu Codebridge dùng Go scanner.
- `codegraph` là tùy chọn; `codegraph_explore` chỉ chạy khi project có `.codegraph/`.
- Tunnel ID và Runtime API key nếu dùng ChatGPT Web tunnel.
- Figma Desktop MCP nếu dùng nhóm tool Figma.
- agentmemory nếu bật project memory.

## Build và cài đặt

```bash
cd codebridge
go test ./...
go build -o dist/codebridge ./cmd/codebridge
```

Hoặc:

```bash
make build
```

Cài vào user bin:

```bash
install -Dm755 dist/codebridge "$HOME/.local/bin/codebridge"
```

Binary cũng hỗ trợ:

```bash
./dist/codebridge install-cli
```

## Bắt đầu nhanh

Chạy wizard:

```bash
codebridge setup
```

Chạy trong repository hiện tại:

```bash
cd /path/to/repo
codebridge
```

Lệnh mặc định tự nhận Git root, lưu workspace và chạy background.

Chỉ chạy local MCP, không dùng tunnel:

```bash
codebridge start \
  --workspace /path/to/repo \
  --no-tunnel \
  --background \
  --save
```

Chạy foreground để debug:

```bash
codebridge serve --workspace /path/to/repo --no-tunnel
```

Các endpoint mặc định:

- MCP: `http://127.0.0.1:8789/mcp`
- Health: `http://127.0.0.1:8789/healthz`
- Supervisor health: `http://127.0.0.1:8789/internal/healthz`

## CLI thường dùng

```bash
codebridge status
codebridge status --json
codebridge doctor
codebridge doctor --json
codebridge workspace
codebridge workspace /path/to/repo
codebridge restart
codebridge stop
codebridge logs
codebridge config get
codebridge config path
codebridge skills list
codebridge figma status
```

Xem toàn bộ command và option:

```bash
codebridge help
```

## ChatGPT Web tunnel

```bash
codebridge tunnel install
codebridge setup
codebridge key set
codebridge profile
codebridge restart
```

`codebridge key set` lưu Runtime API key vào local `.env` với permission `0600`. Trong ChatGPT Web:

1. Mở **Settings → Connectors → Developer mode**.
2. Thêm custom MCP connector.
3. Chọn tunnel đã cấu hình.
4. Chọn `No auth`; Runtime API key nằm trên máy local, không nhập vào connector.
5. Gọi `workspace_info` hoặc `workspace_snapshot` để kiểm tra kết nối.

## Cấu hình và secret

Codebridge nạp cấu hình theo thứ tự:

```text
defaults
  → config.json
  → environment overrides
  → CLI options của lần chạy hiện tại
```

Đường dẫn config:

| Hệ điều hành | Config directory |
|---|---|
| Linux | `$XDG_CONFIG_HOME/codebridge` hoặc `~/.config/codebridge` |
| macOS | `~/Library/Application Support/Codebridge` |
| Windows | `%APPDATA%\Codebridge` |

Các file chính:

```text
config.json   non-secret configuration
.env          Runtime API key và optional memory secret
```

`config.json` và `.env` được ghi với permission `0600` trên Unix. Codebridge không serialize MCP bearer token hoặc approval token vào `config.json`.

Runtime-only secret có thể truyền qua environment:

```bash
export CONTROL_PLANE_API_KEY="..."
export MCP_AUTH_TOKEN="..."
export AGENT_APPROVAL_TOKEN="..."
```

## Figma Desktop

Mặc định Codebridge kết nối:

```text
http://127.0.0.1:3845/mcp
```

Kiểm tra:

```bash
codebridge figma status
codebridge figma tools
```

Remote Figma endpoint bị chặn mặc định. Chỉ bật `FIGMA_DESKTOP_ALLOW_REMOTE=1` khi đã hiểu rủi ro.

# Project memory

Codebridge expose contract trung lập, không để ChatGPT phụ thuộc trực tiếp vào agentmemory:

| Tool | Mục đích |
|---|---|
| `memory_status` | Provider, project scope, capabilities, health và recorder stats |
| `memory_context` | Lấy historical context gọn cho task hiện tại |
| `memory_search` | Tìm decisions, failures, solutions, preferences và procedures |
| `memory_remember` | Lưu một fact/decision/solution rõ ràng |
| `memory_commit` | Tạo session handoff từ summary và local project state |
| `memory_forget` | Xóa memory/session; destructive trong policy `balanced` |
| `memory_export` | Xuất schema canonical dạng object hoặc JSONL |
| `memory_import` | Nhập schema canonical để chuyển backend |

Memory là **historical evidence**, không phải current source of truth. Agent vẫn phải kiểm tra source hiện tại bằng CodeGraph hoặc file tools trước khi sửa code.

## Bật memory

Cách khuyến nghị:

```bash
codebridge setup
codebridge restart
codebridge doctor
```

Setup lưu non-secret settings vào `config.json`. Chỉ secret, nếu backend yêu cầu, được lưu vào `.env`.

Tại prompt secret:

```text
Enter  giữ secret cũ
-      xóa secret
value  thay secret
```

Nếu agentmemory chỉ bind local và không bật `AGENTMEMORY_SECRET`, có thể để secret trống. Khi secret có giá trị, Codebridge gửi:

```text
Authorization: Bearer <secret>
```

## Memory config mẫu

```json
{
  "memory": {
    "enabled": true,
    "provider": "agentmemory",
    "endpoint": "http://127.0.0.1:3111",
    "secretEnv": "CODEBRIDGE_MEMORY_SECRET",
    "timeoutMs": 3000,
    "captureMode": "selected",
    "tokenBudget": 1600,
    "agentId": "chatgpt-codebridge",
    "required": false,
    "projectStrategy": "git-origin",
    "queueSize": 128,
    "deliveryTimeoutMs": 2000,
    "retryMaxAttempts": 3,
    "retryBackoffMs": 100,
    "healthCacheMs": 5000,
    "options": {}
  }
}
```

### `required`

- `false`: fail-open. Provider offline không ngăn Codebridge khởi động hoặc chạy coding tools.
- `true`: startup fail nếu health check không thành công.

### `captureMode`

- `off`: không auto-capture; các tool memory gọi thủ công vẫn hoạt động.
- `selected`: chỉ capture các tool có giá trị lâu dài như edit, test, build, Git, plan, decision và review.
- `metadata`: capture nhiều tool hơn nhưng chỉ giữ metadata tối thiểu.

Codebridge không gửi raw source, patch, command output hoặc secret trong auto-capture. Input được redact; result chỉ lấy các field đã whitelist.

### `projectStrategy`

- `git-origin`: chuẩn hóa remote thành dạng `git:github.com/owner/repo`; clone ở thư mục khác vẫn dùng cùng project memory.
- `path-hash`: hash configured owning root; các checkout khác nhau có memory riêng.

File hoặc thư mục con luôn được quy về configured owning root trước khi tạo project ID, tránh phân mảnh memory theo subdirectory.

### Session identity

Mỗi logical MCP connection có memory session riêng. Codebridge ưu tiên protocol session ID; nếu transport không cung cấp ID, nó tạo process-local hash ổn định từ session object. Nhiều cuộc chat/kết nối đồng thời không bị gom vào một process-level session chung.

### Async recorder

Auto-capture dùng bounded queue và không block tool response:

```text
tool completed
  → redact + whitelist
  → enqueue non-blocking
  → deliver với timeout
  → retry exponential có giới hạn
  → delivered / failed / dropped counters
```

Khi shutdown, recorder đóng nhận mới rồi drain queue còn lại. `memory_status` hiển thị `enqueued`, `delivered`, `retried`, `failed`, `dropped` và queue depth.

## Provider options

`memory.options` dành cho cấu hình adapter, ví dụ custom REST paths hoặc response limit. Codebridge từ chối mọi key có tên chứa `secret`, `password`, `token`, `apiKey`, `authorization` hoặc `credential`; secret phải đi qua `memory.secretEnv`.

Provider mới có thể đăng ký qua `memory/factory.Register` mà không đổi MCP tools hoặc runtime dispatch.

## Export và import

Xuất memory qua MCP:

```json
{
  "path": ".",
  "format": "jsonl"
}
```

Import lại:

```json
{
  "path": ".",
  "jsonl": "{\"id\":\"...\",\"content\":\"...\"}\n"
}
```

Export được normalize sang schema Codebridge. Import replay từng item qua provider `Remember`, thay vì restore raw database dump, nên phù hợp cho migration giữa backend khác nhau.

# Security model

- File tools và command `cwd` chỉ hoạt động trong configured roots.
- Canonicalization chặn traversal, symlink và junction escape.
- Configured root không thể bị delete hoặc rename qua dedicated tools/patch operations.
- Đây không phải OS sandbox; command hợp lệ vẫn chạy với quyền user hiện tại.
- `safe` mode chặn destructive shell patterns và Git mutation.
- `strict` policy chỉ cho phép đọc/phân tích.
- `balanced` cho phép edit nhưng yêu cầu exact one-time approval cho delete, install, network, mutating Git, mutating Figma và `memory_forget`.
- `full` mở project workflow nhưng vẫn chặn catastrophic system commands.
- Audit args được redact đệ quy trước khi ghi local state.
- Non-loopback MCP host bắt buộc có bearer token.

# Cấu trúc repository

```text
cmd/codebridge/       executable entrypoint
internal/app/         version/tier metadata
internal/cli/         command parsing, setup, lifecycle, tunnel và install
internal/server/      HTTP routes, auth, CORS/origin và limits
internal/mcpserver/   MCP SDK adapter, session identity và widget resource
internal/agent/       87-tool registry, runtime, policy và handlers
internal/workspace/   root confinement, owning-root, search và tree
internal/security/    command guards, redaction và approvals
internal/patch/       backup, diff, preview, validate và undo
internal/processx/    bounded process execution và process tree
internal/figma/       Figma Desktop MCP bridge
internal/memory/      canonical contracts, recorder, scoping và adapters
internal/state/       per-workspace local state
internal/assets/      embedded widget và built-in skills
```

Thiết kế chi tiết: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

# Verification

```bash
go test ./...
go test -race ./internal/memory/... ./internal/agent ./internal/cli ./internal/config ./internal/mcpserver
go vet ./...
go build ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=arm64 go build ./...
```

External Streamable HTTP smoke test:

```bash
CODEBRIDGE_TEST_ENDPOINT=http://127.0.0.1:8789/mcp \
  go test ./internal/server -run TestExternalStreamableHTTP -v
```

Codebridge được phát hành theo AGPL-3.0-or-later; xem `LICENSE` ở repository root.
