// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CallIdentity carries transport-level identity into a tool module without
// coupling modules to a specific MCP transport implementation.
type CallIdentity struct {
	SessionID string `json:"session_id,omitempty"`
}

// ToolModule is the extension boundary for a functional group of tools.
// Modules own their tool specifications, routing, health, and lifecycle.
type ToolModule interface {
	Name() string
	Specs() []ToolSpec
	Handle(context.Context, CallIdentity, string, map[string]any) (any, error)
	Health(context.Context) any
	Close() error
}

// ToolCallPolicy lets a module provide an exact approval rule without moving
// shared policy enforcement into the module itself.
type ToolCallPolicy struct {
	ApprovalAction        string
	RequiresApproval      bool
	AlwaysRequireApproval bool
}

type ToolPolicyProvider interface {
	ToolPolicy(string, map[string]any) ToolCallPolicy
}

// ToolAuditProvider lets a module reduce audit data to metadata appropriate for
// its trust boundary. Community MCP modules use this to avoid persisting raw
// arguments or upstream payloads whose schemas Codebridge does not control.
type ToolAuditProvider interface {
	AuditArguments(string, map[string]any) any
	AuditMetadata(string, map[string]any, any) map[string]any
	AuditError(error) string
}

// ToolObservationPolicyProvider can opt a module out of automatic long-term
// memory capture while preserving local audit metadata.
type ToolObservationPolicyProvider interface {
	CaptureObservation(string) bool
}

type builtInModulePolicy struct{}

func (builtInModulePolicy) ToolPolicy(tool string, args map[string]any) ToolCallPolicy {
	action := approvalAction(tool, args)
	return ToolCallPolicy{ApprovalAction: action, RequiresApproval: action != ""}
}

func (r *Runtime) RegisterModule(module ToolModule) error {
	if module == nil {
		return errors.New("tool module is nil")
	}
	name := moduleName(module)
	if name == "" {
		return errors.New("tool module name is required")
	}
	if !validRegistryName(name, 32) {
		return fmt.Errorf("tool module name %q is invalid", name)
	}
	specs := module.Specs()
	if len(specs) == 0 {
		return fmt.Errorf("tool module %q has no tool specifications", name)
	}

	r.moduleMu.Lock()
	defer r.moduleMu.Unlock()
	if r.modulesClosed {
		return errors.New("tool module registry is closed")
	}
	if r.modules == nil {
		r.modules = map[string]ToolModule{}
	}
	if r.toolModules == nil {
		r.toolModules = map[string]ToolModule{}
	}
	if r.toolSpecIndex == nil {
		r.toolSpecIndex = map[string]ToolSpec{}
	}
	if _, exists := r.modules[name]; exists {
		return fmt.Errorf("duplicate tool module %q", name)
	}

	seen := map[string]bool{}
	for _, spec := range specs {
		tool := strings.TrimSpace(spec.Name)
		if tool == "" || tool != spec.Name || !validRegistryName(tool, 64) || spec.Description == "" || spec.Schema == nil {
			return fmt.Errorf("tool module %q has an incomplete or invalid tool specification", name)
		}
		if seen[tool] {
			return fmt.Errorf("tool module %q declares duplicate tool %q", name, tool)
		}
		seen[tool] = true
		if owner := r.toolModules[tool]; owner != nil {
			return fmt.Errorf("tool %q is already registered by module %q", tool, moduleName(owner))
		}
	}

	r.modules[name] = module
	r.moduleOrder = append(r.moduleOrder, name)
	for _, spec := range specs {
		r.toolModules[spec.Name] = module
		r.toolSpecIndex[spec.Name] = spec
		r.toolSpecs = append(r.toolSpecs, spec)
	}
	return nil
}

func (r *Runtime) Tools() []ToolSpec {
	r.moduleMu.RLock()
	defer r.moduleMu.RUnlock()
	return append([]ToolSpec(nil), r.toolSpecs...)
}

func (r *Runtime) ToolSpec(name string) (ToolSpec, bool) {
	r.moduleMu.RLock()
	spec, ok := r.toolSpecIndex[name]
	r.moduleMu.RUnlock()
	return spec, ok
}

func (r *Runtime) ModuleNames() []string {
	r.moduleMu.RLock()
	defer r.moduleMu.RUnlock()
	return append([]string(nil), r.moduleOrder...)
}

func (r *Runtime) Module(name string) (ToolModule, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	r.moduleMu.RLock()
	module, ok := r.modules[name]
	r.moduleMu.RUnlock()
	return module, ok
}

func (r *Runtime) ToolModuleName(tool string) string {
	module, _ := r.ToolModule(tool)
	return moduleName(module)
}

func (r *Runtime) ToolModule(tool string) (ToolModule, bool) {
	r.moduleMu.RLock()
	module, ok := r.toolModules[tool]
	r.moduleMu.RUnlock()
	return module, ok
}

func (r *Runtime) ModuleHealth(ctx context.Context) map[string]any {
	r.moduleMu.RLock()
	names := append([]string(nil), r.moduleOrder...)
	modules := make(map[string]ToolModule, len(names))
	for _, name := range names {
		modules[name] = r.modules[name]
	}
	r.moduleMu.RUnlock()

	result := make(map[string]any, len(modules))
	for _, name := range names {
		result[name] = modules[name].Health(ctx)
	}
	return result
}

func (r *Runtime) ToolEnabled(name string) bool {
	for _, denied := range r.Config.Tools.DeniedTools {
		if denied == name {
			return false
		}
	}
	group := r.ToolModuleName(name)
	if group == "" {
		return false
	}
	if len(r.Config.Tools.AllowedGroups) == 0 {
		return true
	}
	for _, allowed := range r.Config.Tools.AllowedGroups {
		if allowed == group {
			return true
		}
	}
	return false
}

func (r *Runtime) dispatch(ctx context.Context, identity CallIdentity, tool string, args map[string]any) (any, error) {
	r.moduleMu.RLock()
	module := r.toolModules[tool]
	r.moduleMu.RUnlock()
	if module == nil {
		return nil, fmt.Errorf("unknown tool: %s", tool)
	}
	return module.Handle(ctx, identity, tool, args)
}

func (r *Runtime) closeModules() error {
	r.moduleMu.Lock()
	r.modulesClosed = true
	names := append([]string(nil), r.moduleOrder...)
	modules := make(map[string]ToolModule, len(names))
	for _, name := range names {
		modules[name] = r.modules[name]
	}
	r.moduleMu.Unlock()

	var errs []error
	for index := len(names) - 1; index >= 0; index-- {
		name := names[index]
		if err := modules[name].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close module %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func moduleName(module ToolModule) string {
	if module == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(module.Name()))
}

func validRegistryName(value string, maxLength int) bool {
	if value == "" || len(value) > maxLength || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func healthyModule(name string, toolCount int) map[string]any {
	return map[string]any{"module": name, "available": true, "tools": toolCount}
}
