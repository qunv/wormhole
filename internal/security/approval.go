// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"wormhole/internal/state"
)

const (
	approvalRetention     = 30 * 24 * time.Hour
	maxApprovalRecords    = 1024
	maxApprovalRecordSize = 64 << 10
)

var approvalID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type ApprovalRecord struct {
	ID              string   `json:"id"`
	Action          string   `json:"action"`
	Actions         []string `json:"actions"`
	ConsumedActions []string `json:"consumed_actions"`
	Reason          string   `json:"reason"`
	Status          string   `json:"status"`
	Created         string   `json:"created"`
	ExpiresAt       string   `json:"expires_at"`
	ApprovedAt      string   `json:"approved_at,omitempty"`
	DeniedAt        string   `json:"denied_at,omitempty"`
	ConsumedAt      string   `json:"consumed_at,omitempty"`
}

type ApprovalManager struct {
	Store *state.Store
	Token string
	TTL   time.Duration
	mu    sync.Mutex

	loaded  bool
	records map[string]*ApprovalRecord
	active  map[string]map[string]struct{}
}

func NewApprovalManager(store *state.Store, token string, ttl time.Duration) *ApprovalManager {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &ApprovalManager{
		Store: store, Token: token, TTL: ttl,
		records: map[string]*ApprovalRecord{}, active: map[string]map[string]struct{}{},
	}
}

func (m *ApprovalManager) Request(actions []string, reason string, ttl time.Duration) (*ApprovalRecord, error) {
	actions = unique(actions)
	if len(actions) == 0 {
		return nil, errors.New("at least one exact action is required")
	}
	if ttl <= 0 {
		ttl = m.TTL
	}
	id, err := uuid4()
	if err != nil {
		return nil, fmt.Errorf("create approval id: %w", err)
	}
	now := time.Now().UTC()
	record := &ApprovalRecord{
		ID: id, Action: actions[0], Actions: actions, Reason: reason,
		Status: "pending", Created: now.Format(time.RFC3339Nano),
		ExpiresAt: now.Add(ttl).Format(time.RFC3339Nano),
	}
	if len(actions) > 1 {
		record.Action = fmt.Sprintf("batch:%d", len(actions))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoadedLocked(now); err != nil {
		return nil, err
	}
	if err := m.write(record); err != nil {
		return nil, err
	}
	m.records[record.ID] = record
	m.pruneTerminalLocked(now)
	return cloneApproval(record), nil
}

func (m *ApprovalManager) Decide(id, token, decision string) (*ApprovalRecord, error) {
	if m.Token == "" {
		return nil, errors.New("MCP approval is disabled; set AGENT_APPROVAL_TOKEN")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(m.Token)) != 1 {
		return nil, errors.New("invalid local operator approval token")
	}
	return m.decide(id, decision)
}

// DecideLocal applies a decision from an already authenticated local control
// plane. Callers must enforce their own loopback, authentication, origin, and
// CSRF boundary; this method intentionally does not accept or expose the MCP
// operator token.
func (m *ApprovalManager) DecideLocal(id, decision string) (*ApprovalRecord, error) {
	return m.decide(id, decision)
}

func (m *ApprovalManager) decide(id, decision string) (*ApprovalRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !approvalID.MatchString(id) {
		return nil, errors.New("invalid approval id")
	}
	now := time.Now().UTC()
	if err := m.ensureLoadedLocked(now); err != nil {
		return nil, err
	}
	record, err := m.read(id)
	if err != nil {
		return nil, err
	}
	m.records[id] = record
	if record.Status != "pending" {
		return nil, fmt.Errorf("approval is %s; only pending requests can be decided", record.Status)
	}
	if expiredAt(record, now) {
		record.Status = "expired"
		_ = m.write(record)
		m.pruneTerminalLocked(now)
		return nil, errors.New("approval request is expired")
	}
	timestamp := now.Format(time.RFC3339Nano)
	switch decision {
	case "approved":
		record.Status = "approved"
		record.ApprovedAt = timestamp
	case "denied":
		record.Status = "denied"
		record.DeniedAt = timestamp
	default:
		return nil, errors.New("invalid approval decision")
	}
	if err := m.write(record); err != nil {
		return nil, err
	}
	m.removeActiveLocked(record)
	if record.Status == "approved" {
		m.addActiveLocked(record)
	}
	m.pruneTerminalLocked(now)
	return cloneApproval(record), nil
}

// List returns a newest-first bounded snapshot. Status may be empty or "all"
// to include every record, or one exact persisted status.
func (m *ApprovalManager) List(status string, limit int) ([]ApprovalRecord, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = "all"
	}
	if status != "all" && status != "pending" && status != "approved" && status != "denied" && status != "consumed" && status != "expired" {
		return nil, errors.New("invalid approval status filter")
	}
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 200)

	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	if err := m.ensureLoadedLocked(now); err != nil {
		return nil, err
	}
	items := make([]ApprovalRecord, 0, min(len(m.records), limit))
	for id := range m.records {
		record, err := m.read(id)
		if err != nil {
			continue
		}
		m.records[id] = record
		if (record.Status == "pending" || record.Status == "approved") && expiredAt(record, now) {
			record.Status = "expired"
			m.removeActiveLocked(record)
			_ = m.write(record)
		}
		if status != "all" && record.Status != status {
			continue
		}
		items = append(items, *cloneApproval(record))
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := parseRecordTime(items[i].Created), parseRecordTime(items[j].Created)
		if left.Equal(right) {
			return items[i].ID > items[j].ID
		}
		return left.After(right)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	m.pruneTerminalLocked(now)
	return items, nil
}

func (m *ApprovalManager) Consume(action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	if err := m.ensureLoadedLocked(now); err != nil {
		return errors.New("approval required")
	}
	ids := m.active[action]
	var selected *ApprovalRecord
	var selectedAt time.Time
	for id := range ids {
		record, readErr := m.read(id)
		if readErr != nil {
			delete(ids, id)
			delete(m.records, id)
			continue
		}
		m.records[id] = record
		if record.Status != "approved" {
			m.removeActiveLocked(record)
			continue
		}
		if contains(record.ConsumedActions, action) {
			delete(ids, id)
			continue
		}
		if expiredAt(record, now) {
			record.Status = "expired"
			m.removeActiveLocked(record)
			_ = m.write(record)
			continue
		}
		approvedAt := parseRecordTime(record.ApprovedAt)
		if approvedAt.IsZero() {
			approvedAt = parseRecordTime(record.Created)
		}
		if selected == nil || approvedAt.After(selectedAt) {
			selected = record
			selectedAt = approvedAt
		}
	}
	if len(ids) == 0 {
		delete(m.active, action)
	}
	if selected == nil {
		return fmt.Errorf("approval required for exact action %q", action)
	}
	selected.ConsumedActions = append(selected.ConsumedActions, action)
	if allConsumed(selected.Actions, selected.ConsumedActions) {
		selected.Status = "consumed"
		selected.ConsumedAt = now.Format(time.RFC3339Nano)
		m.removeActiveLocked(selected)
	} else if entries := m.active[action]; entries != nil {
		delete(entries, selected.ID)
		if len(entries) == 0 {
			delete(m.active, action)
		}
	}
	if err := m.write(selected); err != nil {
		return err
	}
	m.pruneTerminalLocked(now)
	return nil
}

func (m *ApprovalManager) ensureLoadedLocked(now time.Time) error {
	if m.loaded {
		return nil
	}
	files, err := os.ReadDir(m.Store.ApprovalsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.loaded = true
			return nil
		}
		return err
	}
	type terminalRecord struct {
		id string
		at time.Time
	}
	var terminal []terminalRecord
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		id := file.Name()[:len(file.Name())-5]
		record, readErr := m.read(id)
		if readErr != nil {
			continue
		}
		if (record.Status == "pending" || record.Status == "approved") && expiredAt(record, now) {
			record.Status = "expired"
			_ = m.write(record)
		}
		m.records[id] = record
		if record.Status == "approved" {
			m.addActiveLocked(record)
			continue
		}
		if isTerminalApproval(record.Status) {
			at := approvalTerminalTime(record)
			if !at.IsZero() && now.Sub(at) > approvalRetention {
				_ = os.Remove(filepath.Join(m.Store.ApprovalsDir, id+".json"))
				delete(m.records, id)
				continue
			}
			terminal = append(terminal, terminalRecord{id: id, at: at})
		}
	}
	if len(m.records) > maxApprovalRecords {
		sort.Slice(terminal, func(i, j int) bool { return terminal[i].at.Before(terminal[j].at) })
		removeCount := min(len(terminal), len(m.records)-maxApprovalRecords)
		for _, item := range terminal[:removeCount] {
			_ = os.Remove(filepath.Join(m.Store.ApprovalsDir, item.id+".json"))
			delete(m.records, item.id)
		}
	}
	m.loaded = true
	return nil
}

func (m *ApprovalManager) pruneTerminalLocked(now time.Time) {
	type terminalRecord struct {
		id string
		at time.Time
	}
	var terminal []terminalRecord
	for id, record := range m.records {
		if !isTerminalApproval(record.Status) {
			continue
		}
		at := approvalTerminalTime(record)
		if !at.IsZero() && now.Sub(at) > approvalRetention {
			_ = os.Remove(filepath.Join(m.Store.ApprovalsDir, id+".json"))
			delete(m.records, id)
			continue
		}
		terminal = append(terminal, terminalRecord{id: id, at: at})
	}
	if len(m.records) <= maxApprovalRecords || len(terminal) == 0 {
		return
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].at.Before(terminal[j].at) })
	removeCount := min(len(terminal), len(m.records)-maxApprovalRecords)
	for _, item := range terminal[:removeCount] {
		_ = os.Remove(filepath.Join(m.Store.ApprovalsDir, item.id+".json"))
		delete(m.records, item.id)
	}
}

func (m *ApprovalManager) addActiveLocked(record *ApprovalRecord) {
	for _, action := range record.Actions {
		if contains(record.ConsumedActions, action) {
			continue
		}
		ids := m.active[action]
		if ids == nil {
			ids = map[string]struct{}{}
			m.active[action] = ids
		}
		ids[record.ID] = struct{}{}
	}
}

func (m *ApprovalManager) removeActiveLocked(record *ApprovalRecord) {
	if record == nil {
		return
	}
	for _, action := range record.Actions {
		if ids := m.active[action]; ids != nil {
			delete(ids, record.ID)
			if len(ids) == 0 {
				delete(m.active, action)
			}
		}
	}
}

func (m *ApprovalManager) read(id string) (*ApprovalRecord, error) {
	if !approvalID.MatchString(id) {
		return nil, errors.New("invalid approval id")
	}
	file, err := os.Open(filepath.Join(m.Store.ApprovalsDir, id+".json"))
	if err != nil {
		return nil, fmt.Errorf("approval request not found: %s", id)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxApprovalRecordSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxApprovalRecordSize {
		return nil, errors.New("approval record exceeds size limit")
	}
	var record ApprovalRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (m *ApprovalManager) write(record *ApprovalRecord) error {
	return m.Store.WriteJSON(filepath.Join(m.Store.ApprovalsDir, record.ID+".json"), record)
}

func expiredAt(record *ApprovalRecord, now time.Time) bool {
	value, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
	return err != nil || !value.After(now)
}

func isTerminalApproval(status string) bool {
	return status == "denied" || status == "consumed" || status == "expired"
}

func approvalTerminalTime(record *ApprovalRecord) time.Time {
	for _, value := range []string{record.ConsumedAt, record.DeniedAt, record.ExpiresAt, record.Created} {
		if parsed := parseRecordTime(value); !parsed.IsZero() {
			return parsed
		}
	}
	return time.Time{}
}

func parseRecordTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func cloneApproval(record *ApprovalRecord) *ApprovalRecord {
	if record == nil {
		return nil
	}
	clone := *record
	clone.Actions = append([]string(nil), record.Actions...)
	clone.ConsumedActions = append([]string(nil), record.ConsumedActions...)
	return &clone
}

func uuid4() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	text := hex.EncodeToString(raw)
	return text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:], nil
}

func unique(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if item != "" && !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func allConsumed(actions, consumed []string) bool {
	for _, action := range actions {
		if !contains(consumed, action) {
			return false
		}
	}
	return true
}
