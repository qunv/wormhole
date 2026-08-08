// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"wormhole/internal/agent"
	"wormhole/internal/security"
	"wormhole/internal/workspaceregistry"
)

const (
	maxAdminApprovalItems = 200
	maxAdminAuditItems    = 200
	maxAdminAuditTail     = 2 << 20
)

type adminRuntimeEntry struct {
	ID      string
	Runtime *agent.Runtime
}

func (h *Handler) runtimeEntries() []adminRuntimeEntry {
	items := make([]adminRuntimeEntry, 0, 1+len(h.Runtimes))
	if h.Runtime != nil {
		id := strings.TrimSpace(h.Runtime.WorkspaceID)
		if id == "" {
			id = "default"
		}
		items = append(items, adminRuntimeEntry{ID: id, Runtime: h.Runtime})
	}
	ids := make([]string, 0, len(h.Runtimes))
	for id := range h.Runtimes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		runtime := h.Runtimes[id]
		if runtime == nil || runtime == h.Runtime {
			continue
		}
		items = append(items, adminRuntimeEntry{ID: id, Runtime: runtime})
	}
	return items
}

func (h *Handler) runtimeByID(rawID string) (adminRuntimeEntry, bool) {
	id := workspaceregistry.NormalizeID(rawID)
	for _, item := range h.runtimeEntries() {
		if workspaceregistry.NormalizeID(item.ID) == id {
			return item, true
		}
	}
	return adminRuntimeEntry{}, false
}

func (h *Handler) getOperations(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 8*time.Second)
	defer cancel()
	workspaces := make([]map[string]any, 0, 1+len(h.Runtimes))
	for _, item := range h.runtimeEntries() {
		runtime := item.Runtime
		workspaces = append(workspaces, map[string]any{
			"id": item.ID, "root": runtime.Workspace.Primary,
			"configId": runtime.ConfigID, "mode": runtime.Config.Mode, "policy": runtime.Config.Policy,
			"startupWarnings": runtime.StartupWarnings(),
			"metrics":         runtime.RuntimeMetrics(true, 20),
			"modules":         runtime.ModuleHealth(ctx),
		})
	}
	routerStats := map[string]any{"enabled": false}
	if h.Router != nil {
		routerStats = h.Router.Stats()
		routerStats["enabled"] = true
	}
	shared := map[string]any{"enabled": false}
	if h.Runtime != nil {
		shared = h.Runtime.SharedResourceStats()
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"generatedAt": time.Now().UTC(), "workspaces": workspaces,
		"sharedResources": shared, "sessionRouter": routerStats,
	})
}

type adminApprovalRecord struct {
	WorkspaceID string `json:"workspaceId"`
	Root        string `json:"root"`
	security.ApprovalRecord
}

func (h *Handler) getApprovals(writer http.ResponseWriter, request *http.Request) {
	status := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("status")))
	limit, err := boundedQueryLimit(request, 100, maxAdminApprovalItems)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "approval_query_invalid", err.Error())
		return
	}
	workspaceFilter := strings.TrimSpace(request.URL.Query().Get("workspace"))
	items := make([]adminApprovalRecord, 0, limit)
	for _, runtimeEntry := range h.runtimeEntries() {
		if workspaceFilter != "" && workspaceregistry.NormalizeID(workspaceFilter) != workspaceregistry.NormalizeID(runtimeEntry.ID) {
			continue
		}
		records, listErr := runtimeEntry.Runtime.Approvals.List(status, limit)
		if listErr != nil {
			h.sendError(writer, http.StatusBadRequest, "approval_query_invalid", listErr.Error())
			return
		}
		for _, record := range records {
			items = append(items, adminApprovalRecord{
				WorkspaceID: runtimeEntry.ID, Root: runtimeEntry.Runtime.Workspace.Primary,
				ApprovalRecord: record,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, items[i].Created)
		right, _ := time.Parse(time.RFC3339Nano, items[j].Created)
		if left.Equal(right) {
			return items[i].ID > items[j].ID
		}
		return left.After(right)
	})
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"approvals": items, "count": len(items), "truncated": truncated,
	})
}

func (h *Handler) approvalDecision(writer http.ResponseWriter, request *http.Request, rawPath string) {
	if request.Method != http.MethodPost {
		h.sendError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST to approve or deny an approval request.")
		return
	}
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) != 2 {
		h.sendError(writer, http.StatusBadRequest, "approval_path_invalid", "Expected an approval path containing workspace ID and approval ID.")
		return
	}
	workspaceID, workspaceErr := url.PathUnescape(parts[0])
	approvalID, approvalErr := url.PathUnescape(parts[1])
	if workspaceErr != nil || approvalErr != nil || workspaceID == "" || approvalID == "" {
		h.sendError(writer, http.StatusBadRequest, "approval_path_invalid", "Invalid workspace or approval ID.")
		return
	}
	runtimeEntry, ok := h.runtimeByID(workspaceID)
	if !ok {
		h.sendError(writer, http.StatusNotFound, "workspace_not_found", "The approval workspace is not active.")
		return
	}
	raw, err := readBody(writer, request, maxAuthBody)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	var input struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		h.sendError(writer, http.StatusBadRequest, "invalid_body", "Expected a JSON object containing decision.")
		return
	}
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	if input.Decision != "approved" && input.Decision != "denied" {
		h.sendError(writer, http.StatusUnprocessableEntity, "approval_decision_invalid", "Decision must be approved or denied.")
		return
	}
	record, err := runtimeEntry.Runtime.Approvals.DecideLocal(approvalID, input.Decision)
	if err != nil {
		status := http.StatusConflict
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "invalid approval id") {
			status = http.StatusNotFound
		}
		h.sendError(writer, status, "approval_decision_failed", err.Error())
		return
	}
	h.sendJSON(writer, http.StatusOK, adminApprovalRecord{
		WorkspaceID: runtimeEntry.ID, Root: runtimeEntry.Runtime.Workspace.Primary,
		ApprovalRecord: *record,
	})
}

func (h *Handler) getAudit(writer http.ResponseWriter, request *http.Request) {
	limit, err := boundedQueryLimit(request, 100, maxAdminAuditItems)
	if err != nil {
		h.sendError(writer, http.StatusBadRequest, "audit_query_invalid", err.Error())
		return
	}
	workspaceFilter := strings.TrimSpace(request.URL.Query().Get("workspace"))
	toolFilter := strings.TrimSpace(request.URL.Query().Get("tool"))
	statusFilter := strings.TrimSpace(request.URL.Query().Get("status"))
	records := make([]map[string]any, 0, limit)
	truncated := false
	for _, runtimeEntry := range h.runtimeEntries() {
		if workspaceFilter != "" && workspaceregistry.NormalizeID(workspaceFilter) != workspaceregistry.NormalizeID(runtimeEntry.ID) {
			continue
		}
		flushCtx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		_ = runtimeEntry.Runtime.FlushAudit(flushCtx)
		cancel()
		workspaceRecords, tailTruncated, readErr := readAuditTail(runtimeEntry.Runtime.Store.AuditPath)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			h.sendError(writer, http.StatusInternalServerError, "audit_read_failed", readErr.Error())
			return
		}
		truncated = truncated || tailTruncated
		for _, record := range workspaceRecords {
			if toolFilter != "" && fmt.Sprint(record["tool"]) != toolFilter {
				continue
			}
			if statusFilter != "" && fmt.Sprint(record["status"]) != statusFilter {
				continue
			}
			record["workspaceId"] = runtimeEntry.ID
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return fmt.Sprint(records[i]["ts"]) > fmt.Sprint(records[j]["ts"])
	})
	if len(records) > limit {
		records = records[:limit]
		truncated = true
	}
	h.sendJSON(writer, http.StatusOK, map[string]any{
		"records": records, "count": len(records), "truncated": truncated,
	})
}

func readAuditTail(path string) ([]map[string]any, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("audit path is not a regular file")
	}
	offset := max(int64(0), info.Size()-maxAdminAuditTail)
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxAdminAuditTail+1))
	if err != nil {
		return nil, false, err
	}
	truncated := offset > 0 || len(raw) > maxAdminAuditTail
	if len(raw) > maxAdminAuditTail {
		raw = raw[:maxAdminAuditTail]
	}
	if offset > 0 {
		index := bytes.IndexByte(raw, '\n')
		if index < 0 {
			return nil, true, nil
		}
		raw = raw[index+1:]
	}
	lines := bytes.Split(raw, []byte{'\n'})
	records := make([]map[string]any, 0, len(lines))
	for index := len(lines) - 1; index >= 0; index-- {
		line := bytes.TrimSpace(lines[index])
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if json.Unmarshal(line, &record) != nil || record == nil {
			continue
		}
		records = append(records, record)
	}
	return records, truncated, nil
}

func boundedQueryLimit(request *http.Request, fallback, maximum int) (int, error) {
	value := strings.TrimSpace(request.URL.Query().Get("limit"))
	if value == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 0, errors.New("limit must be a positive integer")
	}
	return min(limit, maximum), nil
}
