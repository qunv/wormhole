// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package noop

import (
	"context"
	"errors"

	"codebridge/internal/memory"
)

type Provider struct{}

func New() *Provider { return &Provider{} }

func (*Provider) Name() string                      { return "none" }
func (*Provider) Capabilities() memory.Capabilities { return memory.Capabilities{} }
func (*Provider) Health(context.Context) memory.HealthResult {
	return memory.HealthResult{Provider: "none", Enabled: false, Available: false}
}
func (*Provider) Search(context.Context, memory.SearchRequest) (memory.SearchResult, error) {
	return memory.SearchResult{}, errors.New("memory is disabled")
}
func (*Provider) Context(context.Context, memory.ContextRequest) (memory.ContextResult, error) {
	return memory.ContextResult{}, errors.New("memory is disabled")
}
func (*Provider) Remember(context.Context, memory.RememberRequest) (memory.RememberResult, error) {
	return memory.RememberResult{}, errors.New("memory is disabled")
}
func (*Provider) Observe(context.Context, memory.ObservationRequest) error {
	return errors.New("memory is disabled")
}
func (*Provider) Forget(context.Context, memory.ForgetRequest) (memory.ForgetResult, error) {
	return memory.ForgetResult{}, errors.New("memory is disabled")
}
func (*Provider) Close() error { return nil }
