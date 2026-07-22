// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"codebridge/internal/state"
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
}

func NewApprovalManager(store *state.Store, token string, ttl time.Duration) *ApprovalManager {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &ApprovalManager{Store: store, Token: token, TTL: ttl}
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
	if err := m.write(record); err != nil {
		return nil, err
	}
	return record, nil
}

func (m *ApprovalManager) Decide(id, token, decision string) (*ApprovalRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Token == "" {
		return nil, errors.New("MCP approval is disabled; set AGENT_APPROVAL_TOKEN")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(m.Token)) != 1 {
		return nil, errors.New("invalid local operator approval token")
	}
	if !approvalID.MatchString(id) {
		return nil, errors.New("invalid approval id")
	}
	record, err := m.read(id)
	if err != nil {
		return nil, err
	}
	if record.Status != "pending" {
		return nil, fmt.Errorf("approval is %s; only pending requests can be decided", record.Status)
	}
	if expired(record) {
		return nil, errors.New("approval request is expired")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch decision {
	case "approved":
		record.Status = "approved"
		record.ApprovedAt = now
	case "denied":
		record.Status = "denied"
		record.DeniedAt = now
	default:
		return nil, errors.New("invalid approval decision")
	}
	if err := m.write(record); err != nil {
		return nil, err
	}
	return record, nil
}

func (m *ApprovalManager) Consume(action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	files, err := os.ReadDir(m.Store.ApprovalsDir)
	if err != nil {
		return errors.New("approval required")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() > files[j].Name() })
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		record, err := m.read(file.Name()[:len(file.Name())-5])
		if err != nil || record.Status != "approved" {
			continue
		}
		if expired(record) {
			record.Status = "expired"
			_ = m.write(record)
			continue
		}
		if !contains(record.Actions, action) || contains(record.ConsumedActions, action) {
			continue
		}
		record.ConsumedActions = append(record.ConsumedActions, action)
		if allConsumed(record.Actions, record.ConsumedActions) {
			record.Status = "consumed"
			record.ConsumedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		return m.write(record)
	}
	return fmt.Errorf("approval required for exact action %q", action)
}

func (m *ApprovalManager) read(id string) (*ApprovalRecord, error) {
	if !approvalID.MatchString(id) {
		return nil, errors.New("invalid approval id")
	}
	raw, err := os.ReadFile(filepath.Join(m.Store.ApprovalsDir, id+".json"))
	if err != nil {
		return nil, fmt.Errorf("approval request not found: %s", id)
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

func expired(record *ApprovalRecord) bool {
	value, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
	return err != nil || !value.After(time.Now())
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
