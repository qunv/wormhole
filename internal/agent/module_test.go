package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"codebridge/internal/config"
)

type testToolModule struct {
	name     string
	specs    []ToolSpec
	identity CallIdentity
	handled  string
	closed   int
	closeErr error
}

func (m *testToolModule) Name() string      { return m.name }
func (m *testToolModule) Specs() []ToolSpec { return m.specs }
func (m *testToolModule) Handle(_ context.Context, identity CallIdentity, tool string, _ map[string]any) (any, error) {
	m.identity = identity
	m.handled = tool
	return map[string]any{"ok": true}, nil
}
func (m *testToolModule) Health(context.Context) any {
	return map[string]any{"available": true}
}
func (m *testToolModule) Close() error { m.closed++; return m.closeErr }

func testModule(name, tool string) *testToolModule {
	return &testToolModule{name: name, specs: []ToolSpec{{
		Name: tool, Title: tool, Description: "test tool", ReadOnly: true, Schema: object(nil),
	}}}
}

func TestRuntimeRegistersFunctionalModules(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = t.TempDir()
	cfg.NoTunnel = true
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	wantModules := []string{"basic", "filesystem", "repo", "workflow", "memory", "execution"}
	if got := runtime.ModuleNames(); !reflect.DeepEqual(got, wantModules) {
		t.Fatalf("module order = %#v, want %#v", got, wantModules)
	}
	if got, want := len(runtime.Tools()), 75; got != want {
		t.Fatalf("runtime tool count = %d, want %d", got, want)
	}
	for tool, want := range map[string]string{
		"ping": "basic", "read_file": "filesystem", "git": "execution",
		"workspace_snapshot": "repo", "task_plan": "workflow", "memory_search": "memory",
	} {
		if got := runtime.ToolModuleName(tool); got != want {
			t.Fatalf("module for %s = %q, want %q", tool, got, want)
		}
	}
}

func TestRegisterModuleRejectsDuplicateNamesAndTools(t *testing.T) {
	runtime := &Runtime{}
	if err := runtime.RegisterModule(testModule("custom/module", "custom_ping")); err == nil || !strings.Contains(err.Error(), "module name") {
		t.Fatalf("invalid module name error = %v", err)
	}
	if err := runtime.RegisterModule(testModule("custom", "custom.tool")); err == nil || !strings.Contains(err.Error(), "invalid tool specification") {
		t.Fatalf("invalid tool name error = %v", err)
	}
	first := testModule("custom", "custom_ping")
	if err := runtime.RegisterModule(first); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RegisterModule(testModule("custom", "another_tool")); err == nil || !strings.Contains(err.Error(), "duplicate tool module") {
		t.Fatalf("duplicate module name error = %v", err)
	}
	if err := runtime.RegisterModule(testModule("another", "custom_ping")); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate tool error = %v", err)
	}
}

func TestModuleDispatchPropagatesCallIdentity(t *testing.T) {
	runtime := &Runtime{}
	module := testModule("custom", "custom_ping")
	if err := runtime.RegisterModule(module); err != nil {
		t.Fatal(err)
	}
	identity := CallIdentity{SessionID: "mcp:test-session"}
	if _, err := runtime.dispatch(context.Background(), identity, "custom_ping", nil); err != nil {
		t.Fatal(err)
	}
	if module.identity != identity || module.handled != "custom_ping" {
		t.Fatalf("identity/tool not propagated: identity=%#v tool=%q", module.identity, module.handled)
	}
	health := runtime.ModuleHealth(context.Background())
	if _, ok := health["custom"]; !ok {
		t.Fatalf("module health omitted custom module: %#v", health)
	}
}

func TestRuntimeCloseClosesModulesOnce(t *testing.T) {
	runtime := &Runtime{}
	module := testModule("custom", "custom_ping")
	if err := runtime.RegisterModule(module); err != nil {
		t.Fatal(err)
	}
	runtime.Close()
	runtime.Close()
	if module.closed != 1 {
		t.Fatalf("module close count = %d, want 1", module.closed)
	}
	if err := runtime.RegisterModule(testModule("late", "late_tool")); err == nil || !strings.Contains(err.Error(), "registry is closed") {
		t.Fatalf("register after close error = %v", err)
	}
}

func TestRuntimeShutdownReturnsModuleCloseErrors(t *testing.T) {
	runtime := &Runtime{}
	module := testModule("custom", "custom_ping")
	module.closeErr = errors.New("close failed")
	if err := runtime.RegisterModule(module); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("shutdown error = %v", err)
	}
	runtime.Close()
	if module.closed != 1 {
		t.Fatalf("module close count after repeated shutdown = %d, want 1", module.closed)
	}
}

func TestStrictPolicyBlocksExternalMutationByToolSpec(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "strict"
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	module := testModule("kubernetes", "kubernetes_apply")
	module.specs[0].ReadOnly = false
	if err := runtime.RegisterModule(module); err != nil {
		t.Fatal(err)
	}
	if err := runtime.enforcePolicy("kubernetes_apply", map[string]any{}); err == nil || !strings.Contains(err.Error(), "policy=strict") {
		t.Fatalf("external mutation was not blocked: %v", err)
	}
}

func TestBalancedPolicyRequiresApprovalForExternalWriteTool(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace, cfg.NoTunnel, cfg.Policy = t.TempDir(), true, "balanced"
	runtime, err := New(cfg, "test", "pro", "test-config")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	module := testModule("kubernetes", "kubernetes_delete")
	module.specs[0].ReadOnly = false
	if err := runtime.RegisterModule(module); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"namespace": "dev", "name": "demo"}
	err = runtime.enforcePolicy("kubernetes_delete", args)
	if err == nil || !strings.Contains(err.Error(), genericToolApprovalAction("kubernetes_delete", args)) {
		t.Fatalf("external write tool did not require exact approval: %v", err)
	}
}

func TestToolExposureUsesModuleOwnership(t *testing.T) {
	runtime := &Runtime{Config: config.Config{Tools: config.ToolExposureConfig{AllowedGroups: []string{"kubernetes"}}}}
	if err := runtime.RegisterModule(testModule("kubernetes", "kubernetes_get")); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RegisterModule(testModule("basic", "ping_test")); err != nil {
		t.Fatal(err)
	}
	if !runtime.ToolEnabled("kubernetes_get") || runtime.ToolEnabled("ping_test") {
		t.Fatalf("module exposure mismatch: kubernetes=%t basic=%t", runtime.ToolEnabled("kubernetes_get"), runtime.ToolEnabled("ping_test"))
	}
	runtime.Config.Tools.DeniedTools = []string{"kubernetes_get"}
	if runtime.ToolEnabled("kubernetes_get") {
		t.Fatal("denied tool remained enabled")
	}
}
