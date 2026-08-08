// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import "context"

type workflowModule struct {
	builtInModulePolicy
	runtime *Runtime
}

func newWorkflowModule(runtime *Runtime) ToolModule { return &workflowModule{runtime: runtime} }
func (*workflowModule) Name() string                { return "workflow" }
func (*workflowModule) Specs() []ToolSpec           { return sharedModuleSpecs("workflow", workflowToolSpecs) }
func (m *workflowModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	return m.runtime.handleWorkflow(ctx, tool, args)
}
func (m *workflowModule) Health(context.Context) any {
	return healthyModule("workflow", len(m.Specs()))
}
func (*workflowModule) Close() error { return nil }

func workflowToolSpecs() []ToolSpec {
	return []ToolSpec{
		rwSpec("task_plan", "Task plan", "Create a persistent task plan.", object(map[string]any{"goal": str("Goal."), "steps": array(str(""))}, "goal", "steps"), false),
		rwSpec("task_state", "Task state", "Read or update the active task plan.", object(map[string]any{"set_step_done": integer(), "add_steps": array(str("")), "status": str("Overall status.")}), false),
		rwSpec("decision_log", "Decision log", "Append a decision and rationale.", object(map[string]any{"decision": str("Decision."), "why": str("Rationale.")}, "decision", "why"), false),
	}
}
