# Codebridge

Codebridge là bản triển khai Go độc lập của Local Coding Agent, tập trung vào một binary CLI duy nhất: quản lý workspace, chạy local MCP server, nối ChatGPT Web tunnel, bridge Figma Desktop MCP và cung cấp đầy đủ 79 coding tools.

Tên project được giữ đúng theo yêu cầu: `codebridge`.

## Trạng thái migration

- Native Go CLI và lifecycle: `setup`, `start`, `stop`, `restart`, `status`, `doctor`, `workspace`, `logs`, `config`, `key`, `skills`, `figma`, `tunnel`.
- Streamable HTTP MCP stateless tại `/mcp`, health tại `/healthz`.
- Đủ contract 79 tools: filesystem, command/process, git, skills, companion app, repo intelligence, CodeGraph navigation, patch/undo, quality gates, review, planner, policy/approval, profile và Figma.
- Root confinement có canonicalization để chặn traversal và symlink escape.
- Exact-action approval dùng một lần cho policy `balanced`.
- MCP Apps widget được embed trực tiếp vào binary.
- Tunnel-client có thể tải từ official GitHub release, kiểm tra SHA256 khi release có `SHA256SUMS.txt`.
- Build được cho Linux, macOS và Windows.

## Yêu cầu

- Go 1.25 trở lên.
- Git để tự nhận git root.
- `rg` là tùy chọn; nếu thiếu sẽ dùng Go scanner.
- `codegraph` là tùy chọn; `codegraph_explore` chỉ chạy khi project root có `.codegraph/`.
- Tunnel ID và Runtime API key nếu dùng ChatGPT Web tunnel.
- Figma Desktop MCP nếu dùng nhóm tool Figma.

## Build

```bash
cd codebridge
go test ./...
go build -o dist/codebridge ./cmd/codebridge
```

Hoặc:

```bash
make
```

## Sử dụng nhanh

Chạy setup:

```bash
./dist/codebridge setup
```

Nếu chỉ muốn local MCP, không dùng tunnel:

```bash
./dist/codebridge start --workspace /path/to/repo --no-tunnel --background --save
```

Daily use trong repo bất kỳ:

```bash
cd /path/to/repo
codebridge
```

Lệnh trên tự lấy git root hiện tại, lưu workspace và chạy background. Các lệnh quản lý:

```bash
codebridge status
codebridge doctor
codebridge workspace
codebridge restart
codebridge stop
codebridge logs
```

Chạy server foreground để debug:

```bash
codebridge serve --workspace /path/to/repo --no-tunnel
```

Endpoint:

- MCP: `http://127.0.0.1:8789/mcp`
- Health: `http://127.0.0.1:8789/healthz`

## Tunnel và secret

```bash
codebridge keys
codebridge tunnel install
codebridge key set
codebridge profile
codebridge start --background
```

`config.json` không lưu MCP bearer token, approval token hoặc Runtime API key. Runtime key được lưu riêng với permission `0600`, hoặc có thể truyền bằng environment:

```bash
export CONTROL_PLANE_API_KEY="..."
export MCP_AUTH_TOKEN="..."
export AGENT_APPROVAL_TOKEN="..."
```

## ChatGPT Web Connector

Trong ChatGPT Web:

1. Settings → Connectors → Developer mode.
2. Add custom MCP connector.
3. Chọn tunnel đã cấu hình.
4. Auth chọn `No auth`; Runtime API key nằm ở local machine, không nhập vào connector.
5. Gọi `workspace_info` hoặc `workspace_snapshot` để verify.

## Figma Desktop

Mặc định Codebridge kết nối `http://127.0.0.1:3845/mcp`.

```bash
codebridge figma status
codebridge figma tools
```

Endpoint remote bị chặn mặc định. Chỉ bật `FIGMA_DESKTOP_ALLOW_REMOTE=1` khi đã hiểu rủi ro.

## Security model

- File tools và command `cwd` chỉ hoạt động trong các roots đã cấu hình.
- Symlink/junction escape bị chặn bằng canonical path.
- Đây không phải OS sandbox; command được phép vẫn chạy với quyền của user hiện tại.
- `safe` mode chặn destructive shell patterns và git mutation.
- `strict` policy chỉ cho phép đọc/phân tích.
- `balanced` cho phép edit nhưng yêu cầu exact one-time approval cho delete, install, network, git mutation và mutating Figma calls.
- `full` mở workflow dự án nhưng vẫn chặn catastrophic system commands.

## Cấu trúc

```text
cmd/codebridge/       executable entrypoint
internal/app/         composition metadata
internal/cli/         command parsing, setup, lifecycle, tunnel, release install
internal/server/      HTTP routes, auth, CORS/origin, graceful shutdown
internal/mcpserver/   MCP SDK adapter, resource và 78 tool registrations
internal/agent/       shared runtime và tool handlers
internal/workspace/   root confinement, search, tree
internal/security/    command guards, redaction, approvals
internal/patch/       backup, unified diff, preview, validate, undo
internal/processx/    bounded foreground/background process execution
internal/figma/       official Figma Desktop MCP bridge
internal/state/       per-workspace state store
internal/assets/      embedded MCP Apps widget và built-in skills
```

Thiết kế chi tiết: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Verification

```bash
go test ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=arm64 go build ./...
```

External Streamable HTTP smoke test:

```bash
CODEBRIDGE_TEST_ENDPOINT=http://127.0.0.1:8789/mcp \
  go test ./internal/server -run TestExternalStreamableHTTP -v
```

Project nằm trong repository AGPL-3.0-or-later hiện tại và tuân theo file `LICENSE` ở repository root.
