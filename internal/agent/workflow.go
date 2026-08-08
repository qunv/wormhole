// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type taskStep struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type taskPlan struct {
	Goal    string     `json:"goal"`
	Status  string     `json:"status"`
	Steps   []taskStep `json:"steps"`
	Created string     `json:"created"`
	Updated string     `json:"updated"`
}

func (r *Runtime) handleWorkflow(_ context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "task_plan":
		goal, steps := stringArg(args, "goal", ""), stringsArg(args, "steps")
		if goal == "" || len(steps) == 0 {
			return nil, errors.New("goal and at least one step are required")
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		plan := taskPlan{Goal: goal, Status: "in_progress", Created: now, Updated: now}
		for _, step := range steps {
			plan.Steps = append(plan.Steps, taskStep{Text: step})
		}
		path := r.statePath("current-task.json")
		if err := r.Store.WriteJSON(path, plan); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "goal": goal, "steps_count": len(steps), "path": path}, nil
	case "task_state":
		path := r.statePath("current-task.json")
		var plan taskPlan
		if err := readJSONFile(path, &plan); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return map[string]any{"found": false, "message": "No task plan found. Call task_plan."}, nil
			}
			return nil, err
		}
		changed := false
		if raw, exists := args["set_step_done"]; exists {
			index := intArg(map[string]any{"index": raw}, "index", -1)
			if index < 0 || index >= len(plan.Steps) {
				return nil, fmt.Errorf("step index out of range: %d", index)
			}
			plan.Steps[index].Done, changed = true, true
		}
		for _, step := range stringsArg(args, "add_steps") {
			plan.Steps = append(plan.Steps, taskStep{Text: step})
			changed = true
		}
		if status, exists := args["status"].(string); exists {
			plan.Status, changed = status, true
		}
		if changed {
			plan.Updated = time.Now().UTC().Format(time.RFC3339Nano)
			if err := r.Store.WriteJSON(path, plan); err != nil {
				return nil, err
			}
		}
		done := 0
		for _, step := range plan.Steps {
			if step.Done {
				done++
			}
		}
		return map[string]any{
			"goal": plan.Goal, "status": plan.Status, "steps": plan.Steps,
			"created": plan.Created, "updated": plan.Updated,
			"progress": fmt.Sprintf("%d/%d", done, len(plan.Steps)),
		}, nil
	case "decision_log":
		if err := required(args, "decision", "why"); err != nil {
			return nil, err
		}
		path := r.statePath("decisions.md")
		entry := fmt.Sprintf("\n## %s\n\n**Decision:** %s\n\n**Why:** %s\n",
			time.Now().UTC().Format(time.RFC3339), stringArg(args, "decision", ""), stringArg(args, "why", ""))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		if _, err := file.WriteString(entry); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "appended_to": path}, nil
	default:
		return nil, fmt.Errorf("unsupported workflow tool: %s", name)
	}
}
