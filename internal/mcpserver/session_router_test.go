package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codebridge/internal/agent"
	"codebridge/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkspaceInstructionsPreventExternalContainerFallback(t *testing.T) {
	for name, instructions := range map[string]string{
		"base":    Instructions,
		"session": SessionInstructions,
	} {
		for _, required := range []string{"Codebridge tools", "container", "ENOENT", "reselect the workspace"} {
			if !strings.Contains(instructions, required) {
				t.Fatalf("%s instructions missing %q: %s", name, required, instructions)
			}
		}
	}

	runtime := newSessionTestRuntime(t, "codebridge", t.TempDir())
	router := NewSessionRouter(runtime, nil)
	chat := connectSessionGateway(t, router)
	selected := callSessionTool(t, chat, "workspace_select", map[string]any{"id": "codebridge"}, false)
	object := resultObject(t, selected)
	if object["workspace_access"] != "codebridge_tools_only" {
		t.Fatalf("workspace access marker = %#v", object["workspace_access"])
	}
	instruction, _ := object["instruction"].(string)
	if !strings.Contains(instruction, "Never use ChatGPT's container") {
		t.Fatalf("selection response omitted container warning: %#v", object)
	}

	binding, _ := object["workspace_binding"].(string)
	current := callSessionTool(t, chat, "workspace_current", map[string]any{"workspace_binding": binding}, false)
	currentObject := resultObject(t, current)
	if currentObject["workspace_access"] != "codebridge_tools_only" ||
		!strings.Contains(fmt.Sprint(currentObject["instruction"]), "ENOENT") {
		t.Fatalf("current workspace response omitted access contract: %#v", currentObject)
	}
}

func TestSessionRouterUsesPrimaryRuntimeWorkspaceID(t *testing.T) {
	primary := newSessionTestRuntime(t, "codebridge", t.TempDir())
	api := newSessionTestRuntime(t, "api", t.TempDir())
	router := NewSessionRouter(primary, map[string]*agent.Runtime{"api": api})

	ids := router.workspaceIDs()
	if len(ids) != 2 || ids[0] != "codebridge" || ids[1] != "api" {
		t.Fatalf("workspace IDs = %#v", ids)
	}
	binding, runtime, err := router.selectWorkspace("chat", "codebridge")
	if err != nil {
		t.Fatal(err)
	}
	if binding.WorkspaceID != "codebridge" || runtime != primary {
		t.Fatalf("primary selection = %#v runtime=%p, want %p", binding, runtime, primary)
	}
	items := router.workspaceList("chat", binding.Token)
	if items[0]["id"] != "codebridge" || items[0]["selected"] != true {
		t.Fatalf("primary workspace list item = %#v", items[0])
	}
}

func TestSessionGatewayRoutesTwoChatsToDifferentWorkspaces(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	aRoot := t.TempDir()
	bRoot := t.TempDir()
	aRuntime := newSessionTestRuntime(t, "a", aRoot)
	bRuntime := newSessionTestRuntime(t, "b", bRoot)
	router := NewSessionRouter(defaultRuntime, map[string]*agent.Runtime{"a": aRuntime, "b": bRuntime})

	chatA := connectSessionGateway(t, router)
	chatB := connectSessionGateway(t, router)

	aBinding := selectWorkspaceForTest(t, chatA, "a")
	bBinding := selectWorkspaceForTest(t, chatB, "b")
	if aBinding == bBinding || aBinding == "" || bBinding == "" {
		t.Fatalf("bindings are not distinct: a=%q b=%q", aBinding, bBinding)
	}

	callSessionTool(t, chatA, "write_file", map[string]any{
		"workspace_binding": aBinding, "path": "chat.txt", "content": "from-a",
	}, false)
	callSessionTool(t, chatB, "write_file", map[string]any{
		"workspace_binding": bBinding, "path": "chat.txt", "content": "from-b",
	}, false)

	assertSessionFile(t, filepath.Join(aRoot, "chat.txt"), "from-a")
	assertSessionFile(t, filepath.Join(bRoot, "chat.txt"), "from-b")
	if err := aRuntime.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	aAudit, err := os.ReadFile(aRuntime.Store.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(aAudit), aBinding) || strings.Contains(string(aAudit), "workspace_binding") {
		t.Fatalf("raw binding leaked into audit: %s", aAudit)
	}
	if !strings.Contains(string(aAudit), `"session_id":"workspace:a:chat:`) {
		t.Fatalf("audit did not use binding-scoped session identity: %s", aAudit)
	}

	aCurrent := callSessionTool(t, chatA, "workspace_current", map[string]any{"workspace_binding": aBinding}, false)
	bCurrent := callSessionTool(t, chatB, "workspace_current", map[string]any{"workspace_binding": bBinding}, false)
	if resultObject(t, aCurrent)["workspace_id"] != "a" || resultObject(t, bCurrent)["workspace_id"] != "b" {
		t.Fatalf("wrong current workspaces: a=%s b=%s", resultText(aCurrent), resultText(bCurrent))
	}
}

func TestSessionRouterCreatesDistinctChatSessionsOnSameTransport(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	apiRuntime := newSessionTestRuntime(t, "api", t.TempDir())
	router := NewSessionRouter(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime})

	first, _, err := router.selectWorkspace("shared-transport", "api")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := router.selectWorkspace("shared-transport", "api")
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token || first.SessionID == second.SessionID {
		t.Fatalf("same transport reused chat identity: first=%#v second=%#v", first, second)
	}
	if strings.Contains(first.SessionID, first.Token) || strings.Contains(second.SessionID, second.Token) {
		t.Fatal("binding token leaked into runtime session identity")
	}
}

func TestSessionGatewayBindingSurvivesTransportReconnect(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	apiRoot := t.TempDir()
	apiRuntime := newSessionTestRuntime(t, "api", apiRoot)
	router := NewSessionRouter(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime})

	firstChat := connectSessionGateway(t, router)
	binding := selectWorkspaceForTest(t, firstChat, "API")
	firstChat.Close()

	reconnected := connectSessionGateway(t, router)
	info := callSessionTool(t, reconnected, "workspace_info", map[string]any{
		"workspace_binding": binding,
	}, false)
	if !strings.Contains(resultText(info), `"workspace_id":"api"`) {
		t.Fatalf("reconnected call used wrong runtime: %s", resultText(info))
	}
	callSessionTool(t, reconnected, "write_file", map[string]any{
		"workspace_binding": binding, "path": "reconnected.txt", "content": "ok",
	}, false)
	assertSessionFile(t, filepath.Join(apiRoot, "reconnected.txt"), "ok")
}

func TestSessionGatewaySwitchInvalidatesOldToken(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	aRuntime := newSessionTestRuntime(t, "a", t.TempDir())
	bRuntime := newSessionTestRuntime(t, "b", t.TempDir())
	router := NewSessionRouter(defaultRuntime, map[string]*agent.Runtime{"a": aRuntime, "b": bRuntime})
	chat := connectSessionGateway(t, router)

	aBinding := selectWorkspaceForTest(t, chat, "a")
	bBinding := selectWorkspaceForTest(t, chat, "b")
	if aBinding == bBinding {
		t.Fatalf("workspace switch reused binding %q", aBinding)
	}

	oldInfo := callSessionTool(t, chat, "workspace_info", map[string]any{"workspace_binding": aBinding}, true)
	newInfo := callSessionTool(t, chat, "workspace_info", map[string]any{"workspace_binding": bBinding}, false)
	if !strings.Contains(resultText(oldInfo), "invalid or expired") {
		t.Fatalf("old token remained valid after switch: %s", resultText(oldInfo))
	}
	if !strings.Contains(resultText(newInfo), `"workspace_id":"b"`) {
		t.Fatalf("new token did not select workspace b: %s", resultText(newInfo))
	}
}

func TestSessionGatewayRejectsMissingExpiredAndClearedBindings(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	apiRuntime := newSessionTestRuntime(t, "api", t.TempDir())
	router := NewSessionRouter(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime})
	chat := connectSessionGateway(t, router)

	missing := callSessionTool(t, chat, "workspace_info", map[string]any{}, true)
	if !strings.Contains(resultText(missing), "workspace_binding is required") {
		t.Fatalf("missing binding error = %s", resultText(missing))
	}

	binding := selectWorkspaceForTest(t, chat, "api")
	cleared := callSessionTool(t, chat, "workspace_clear", map[string]any{"workspace_binding": binding}, false)
	if resultObject(t, cleared)["cleared"] != true {
		t.Fatalf("workspace_clear result = %s", resultText(cleared))
	}
	invalid := callSessionTool(t, chat, "workspace_info", map[string]any{"workspace_binding": binding}, true)
	if !strings.Contains(resultText(invalid), "invalid or expired") {
		t.Fatalf("cleared binding remained valid: %s", resultText(invalid))
	}

	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	router.now = func() time.Time { return now }
	expiring := selectWorkspaceForTest(t, chat, "api")
	now = now.Add(defaultBindingTTL + time.Second)
	expired := callSessionTool(t, chat, "workspace_info", map[string]any{"workspace_binding": expiring}, true)
	if !strings.Contains(resultText(expired), "invalid or expired") {
		t.Fatalf("expired binding remained valid: %s", resultText(expired))
	}
}

func TestSessionGatewayToolContractRequiresBinding(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	router := NewSessionRouter(defaultRuntime, nil)
	chat := connectSessionGateway(t, router)
	list, err := chat.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(list.Tools), len(defaultRuntime.Tools())+4; got != want {
		t.Fatalf("gateway tools = %d, want %d", got, want)
	}
	var readFile *mcp.Tool
	var selectTool *mcp.Tool
	for _, tool := range list.Tools {
		switch tool.Name {
		case "read_file":
			readFile = tool
		case "workspace_select":
			selectTool = tool
		}
	}
	if readFile == nil || selectTool == nil {
		t.Fatalf("gateway contract missing tools: read_file=%v workspace_select=%v", readFile != nil, selectTool != nil)
	}
	readSchema, ok := readFile.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("read_file schema type = %T", readFile.InputSchema)
	}
	selectSchema, ok := selectTool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("workspace_select schema type = %T", selectTool.InputSchema)
	}
	required := schemaStrings(readSchema["required"])
	if !containsString(required, "workspace_binding") {
		t.Fatalf("read_file does not require workspace_binding: %#v", readFile.InputSchema)
	}
	if containsString(schemaStrings(selectSchema["required"]), "workspace_binding") {
		t.Fatalf("workspace_select unexpectedly requires a binding: %#v", selectTool.InputSchema)
	}
}

func TestSessionRouterStatsDoNotExposeTokens(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	router := NewSessionRouter(defaultRuntime, nil)
	chat := connectSessionGateway(t, router)
	binding := selectWorkspaceForTest(t, chat, "default")
	stats := router.Stats()
	raw, _ := json.Marshal(stats)
	if strings.Contains(string(raw), binding) {
		t.Fatalf("router stats exposed binding token: %s", raw)
	}
	if stats["active_bindings"] != 1 || stats["bound_sessions"] != 1 {
		t.Fatalf("unexpected router stats: %#v", stats)
	}
}

func TestSessionRouterCapsBindingsAndEvictsOldest(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	router := NewSessionRouter(defaultRuntime, nil)
	router.maxBindings = 2
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	router.now = func() time.Time { return now }
	first, _, err := router.selectWorkspace("chat-1", "default")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, _, err := router.selectWorkspace("chat-2", "default"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, _, err := router.selectWorkspace("chat-3", "default"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := router.resolve("", first.Token); err == nil {
		t.Fatal("oldest binding was not evicted")
	}
	stats := router.Stats()
	if stats["active_bindings"] != 2 || stats["evicted_bindings"] != uint64(1) {
		t.Fatalf("unexpected capacity stats: %#v", stats)
	}
}

func TestSessionRouterExplicitResolveDoesNotGrowSessionIndex(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	router := NewSessionRouter(defaultRuntime, nil)
	binding, _, err := router.selectWorkspace("selected-session", "default")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		if _, _, err := router.resolve(fmt.Sprintf("reconnect-%d", index), binding.Token); err != nil {
			t.Fatal(err)
		}
	}
	stats := router.Stats()
	if stats["bound_sessions"] != 1 {
		t.Fatalf("explicit reconnects grew session index: %#v", stats)
	}
}

func TestSessionRouterUsesSmallestBodyLimit(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	apiRuntime := newSessionTestRuntime(t, "api", t.TempDir())
	defaultRuntime.Config.MaxBodyBytes = 1024
	apiRuntime.Config.MaxBodyBytes = 64
	router := NewSessionRouter(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime})
	if got, want := router.BodyLimit(), 64; got != want {
		t.Fatalf("BodyLimit() = %d, want %d", got, want)
	}
}

func TestSessionRouterMergesConflictingToolContractsConservatively(t *testing.T) {
	defaultRuntime := newSessionTestRuntime(t, "default", t.TempDir())
	apiRuntime := newSessionTestRuntime(t, "api", t.TempDir())
	if err := defaultRuntime.RegisterModule(&sessionContractModule{spec: agent.ToolSpec{
		Name: "workspace_variant", Title: "Variant", Description: "Default variant.", ReadOnly: true,
		Schema: map[string]any{
			"type": "object", "properties": map[string]any{"alpha": map[string]any{"type": "string"}},
			"additionalProperties": false,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := apiRuntime.RegisterModule(&sessionContractModule{name: "variant_api", spec: agent.ToolSpec{
		Name: "workspace_variant", Title: "Variant", Description: "API variant.",
		ReadOnly: false, Destructive: true, OpenWorld: true,
		Schema: map[string]any{
			"type": "object", "properties": map[string]any{"beta": map[string]any{"type": "integer"}},
			"additionalProperties": false,
		},
	}}); err != nil {
		t.Fatal(err)
	}

	router := NewSessionRouter(defaultRuntime, map[string]*agent.Runtime{"api": apiRuntime})
	var merged agent.ToolSpec
	for _, spec := range router.specs {
		if spec.Name == "workspace_variant" {
			merged = spec
			break
		}
	}
	if merged.Name == "" {
		t.Fatal("merged tool was not exposed")
	}
	if merged.ReadOnly || !merged.Destructive || !merged.OpenWorld {
		t.Fatalf("annotations were not merged conservatively: %#v", merged)
	}
	if merged.Schema["additionalProperties"] != true || !strings.Contains(merged.Description, "Arguments vary by workspace") {
		t.Fatalf("conflicting schema was not generalized: %#v", merged)
	}

	chat := connectSessionGateway(t, router)
	defaultBinding := selectWorkspaceForTest(t, chat, "default")
	callSessionTool(t, chat, "workspace_variant", map[string]any{"workspace_binding": defaultBinding, "alpha": "ok"}, false)
	invalidDefault := callSessionTool(t, chat, "workspace_variant", map[string]any{"workspace_binding": defaultBinding, "beta": 2}, true)
	if !strings.Contains(resultText(invalidDefault), "invalid arguments") || !strings.Contains(resultText(invalidDefault), "default") {
		t.Fatalf("default workspace schema was not enforced: %s", resultText(invalidDefault))
	}

	apiBinding := selectWorkspaceForTest(t, chat, "api")
	callSessionTool(t, chat, "workspace_variant", map[string]any{"workspace_binding": apiBinding, "beta": 2}, false)
	invalidAPI := callSessionTool(t, chat, "workspace_variant", map[string]any{"workspace_binding": apiBinding, "alpha": "wrong"}, true)
	if !strings.Contains(resultText(invalidAPI), "invalid arguments") || !strings.Contains(resultText(invalidAPI), "api") {
		t.Fatalf("API workspace schema was not enforced: %s", resultText(invalidAPI))
	}
}

type sessionContractModule struct {
	name string
	spec agent.ToolSpec
}

func (m *sessionContractModule) Name() string {
	if m.name != "" {
		return m.name
	}
	return "variant_default"
}

func (m *sessionContractModule) Specs() []agent.ToolSpec { return []agent.ToolSpec{m.spec} }

func (*sessionContractModule) Handle(context.Context, agent.CallIdentity, string, map[string]any) (any, error) {
	return map[string]any{"ok": true}, nil
}

func (*sessionContractModule) Health(context.Context) any { return map[string]any{"available": true} }

func (*sessionContractModule) Close() error { return nil }

type sessionGatewayClient struct {
	session       *mcp.ClientSession
	serverSession *mcp.ServerSession
}

func connectSessionGateway(t *testing.T, router *SessionRouter) *sessionGatewayClient {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewSessionGateway(router).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "session-router-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		serverSession.Close()
		t.Fatal(err)
	}
	value := &sessionGatewayClient{session: session, serverSession: serverSession}
	t.Cleanup(value.Close)
	return value
}

func (c *sessionGatewayClient) Close() {
	if c == nil {
		return
	}
	if c.session != nil {
		_ = c.session.Close()
		c.session = nil
	}
	if c.serverSession != nil {
		_ = c.serverSession.Close()
		c.serverSession = nil
	}
}

func selectWorkspaceForTest(t *testing.T, client *sessionGatewayClient, id string) string {
	t.Helper()
	result := callSessionTool(t, client, "workspace_select", map[string]any{"id": id}, false)
	binding, _ := resultObject(t, result)["workspace_binding"].(string)
	if binding == "" {
		t.Fatalf("workspace_select returned no binding: %s", resultText(result))
	}
	return binding
}

func callSessionTool(t *testing.T, client *sessionGatewayClient, name string, args map[string]any, wantError bool) *mcp.CallToolResult {
	t.Helper()
	result, err := client.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s protocol error: %v", name, err)
	}
	if result.IsError != wantError {
		t.Fatalf("%s IsError=%t want=%t result=%s", name, result.IsError, wantError, resultText(result))
	}
	return result
}

func resultObject(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if object, ok := result.StructuredContent.(map[string]any); ok {
		return object
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(resultText(result)), &object); err != nil {
		t.Fatalf("decode result object: %v result=%s", err, resultText(result))
	}
	return object
}

func newSessionTestRuntime(t *testing.T, id, root string) *agent.Runtime {
	t.Helper()
	cfg := config.Default()
	cfg.Workspace = root
	cfg.NoTunnel = true
	cfg.Policy = "full"
	runtime, err := agent.NewWorkspaceContextWithReporter(
		context.Background(), id, t.TempDir(), cfg, "test", "pro", "config-"+id, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime
}

func assertSessionFile(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("%s = %q, want %q", path, raw, want)
	}
}

func schemaStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}
