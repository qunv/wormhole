// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

type routedSchemaValidator struct {
	resolved *jsonschema.Resolved
	err      error
}

func compileToolSchema(schema map[string]any) routedSchemaValidator {
	if schema == nil {
		return routedSchemaValidator{err: fmt.Errorf("tool input schema is missing")}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return routedSchemaValidator{err: fmt.Errorf("marshal tool input schema: %w", err)}
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return routedSchemaValidator{err: fmt.Errorf("parse tool input schema: %w", err)}
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return routedSchemaValidator{err: fmt.Errorf("resolve tool input schema: %w", err)}
	}
	return routedSchemaValidator{resolved: resolved}
}

func (r *SessionRouter) validateToolArguments(workspaceID, tool string, schema map[string]any, args map[string]any) error {
	key := workspaceID + "\x00" + tool
	cached, ok := r.schemaValidators.Load(key)
	if !ok {
		compiled := compileToolSchema(schema)
		cached, _ = r.schemaValidators.LoadOrStore(key, compiled)
	}
	validator := cached.(routedSchemaValidator)
	if validator.err != nil {
		return fmt.Errorf("tool %q in workspace %q has an invalid input contract: %w", tool, workspaceID, validator.err)
	}
	if err := validator.resolved.Validate(args); err != nil {
		return fmt.Errorf("invalid arguments for tool %q in workspace %q: %w", tool, workspaceID, err)
	}
	return nil
}
