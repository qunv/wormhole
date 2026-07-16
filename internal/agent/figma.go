// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"fmt"

	"codebridge/internal/figma"
)

func (r *Runtime) handleFigma(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "figma_status":
		return r.Figma.Status(ctx), nil
	case "figma_list_tools":
		tools, err := r.Figma.ListTools(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"endpoint": r.Figma.Endpoint, "count": len(tools), "tools": tools}, nil
	case "figma_call_tool":
		return r.Figma.Call(ctx, stringArg(args, "tool", ""), decodeMap(args["arguments"]))
	default:
		upstream := map[string]string{
			"figma_get_design_context":   "get_design_context",
			"figma_get_screenshot":       "get_screenshot",
			"figma_get_metadata":         "get_metadata",
			"figma_get_variable_defs":    "get_variable_defs",
			"figma_get_code_connect_map": "get_code_connect_map",
			"figma_get_figjam":           "get_figjam",
		}[name]
		if upstream == "" {
			return nil, fmt.Errorf("unsupported Figma tool: %s", name)
		}
		return r.Figma.Call(ctx, upstream, figma.BuildArguments(args))
	}
}
