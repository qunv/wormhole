// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import "context"

type figmaModule struct {
	builtInModulePolicy
	runtime *Runtime
}

func newFigmaModule(runtime *Runtime) ToolModule { return &figmaModule{runtime: runtime} }
func (*figmaModule) Name() string                { return "figma" }
func (*figmaModule) Specs() []ToolSpec           { return figmaToolSpecs() }
func (m *figmaModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	return m.runtime.handleFigma(ctx, tool, args)
}
func (m *figmaModule) Health(ctx context.Context) any {
	status := m.runtime.Figma.Status(ctx)
	status["module"] = "figma"
	status["available"] = status["connected"] == true
	status["registered_tools"] = len(figmaToolSpecs())
	return status
}
func (*figmaModule) Close() error { return nil }

func figmaToolSpecs() []ToolSpec {
	return []ToolSpec{
		roSpec("figma_status", "Figma Desktop status", "Check the official Figma Desktop MCP bridge.", object(nil)),
		roSpec("figma_list_tools", "List Figma tools", "List live upstream Figma MCP tools and schemas.", object(nil)),
		rwSpec("figma_call_tool", "Call Figma tool", "Forward a call to a live Figma Desktop MCP tool.", object(map[string]any{"tool": str("Exact upstream tool name."), "arguments": object(nil)}, "tool"), false),
		roSpec("figma_get_design_context", "Figma design context", "Get implementation-oriented Figma design context.", figmaReadSchema()),
		roSpec("figma_get_screenshot", "Figma screenshot", "Get a Figma node or selection screenshot.", figmaReadSchema()),
		roSpec("figma_get_metadata", "Figma metadata", "Get sparse Figma layer metadata.", figmaReadSchema()),
		roSpec("figma_get_variable_defs", "Figma variables", "Get Figma variables and styles.", figmaReadSchema()),
		roSpec("figma_get_code_connect_map", "Figma Code Connect map", "Get Figma Code Connect mappings.", figmaReadSchema()),
		roSpec("figma_get_figjam", "FigJam context", "Get FigJam XML context.", figmaReadSchema()),
	}
}
