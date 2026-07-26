// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import "context"

type executionModule struct {
	builtInModulePolicy
	runtime *Runtime
}

func newExecutionModule(runtime *Runtime) ToolModule { return &executionModule{runtime: runtime} }
func (*executionModule) Name() string                { return "execution" }
func (*executionModule) Specs() []ToolSpec           { return sharedModuleSpecs("execution", executionToolSpecs) }
func (m *executionModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	return m.runtime.handleExec(ctx, tool, args)
}
func (m *executionModule) Health(context.Context) any {
	return map[string]any{
		"module": "execution", "available": true, "tools": len(m.Specs()),
		"managed_processes": len(m.runtime.Processes.List()),
	}
}
func (m *executionModule) Close() error {
	m.runtime.Processes.StopAll()
	return nil
}

func executionToolSpecs() []ToolSpec {
	command := commandSchema()
	return []ToolSpec{
		rwSpec("run_command", "Run command", "Run a bounded shell command and capture output.", command, false),
		rwSpec("run_commands", "Run commands", "Run up to 12 commands sequentially or concurrently.", object(map[string]any{"commands": array(command), "parallel": boolean(), "max_concurrency": integer(), "stop_on_failure": boolean()}, "commands"), false),
		rwSpec("proc_start", "Start process", "Start a managed background process.", command, false),
		roSpec("proc_list", "List processes", "List managed background processes.", object(nil)),
		roSpec("proc_output", "Process output", "Read buffered process output.", object(map[string]any{"id": str("Process ID."), "tail_chars": integer()}, "id")),
		rwSpec("proc_stop", "Stop process", "Stop a managed process tree.", object(map[string]any{"id": str("Process ID.")}, "id"), true),
		rwSpec("git", "Git", "Run a guarded git command.", object(map[string]any{"args": array(str("")), "cwd": str("Repo directory.")}, "args"), true),
		structuredROSpec("git_status", "Git status", "Return parsed working-tree status, optionally bypassing the short cache.", object(map[string]any{"cwd": str("Repo directory."), "refresh": boolean()})),
		roSpec("git_diff", "Git diff", "Return a bounded git diff.", object(map[string]any{"path": str("Optional path."), "staged": boolean(), "cwd": str("Repo directory.")})),
	}
}
