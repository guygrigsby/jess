package jess

import (
	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/gate"
	"github.com/guygrigsby/jess/internal/core"
)

// SafeTool is the marker a tool implements to be auto-approved by the gate.
type SafeTool = gate.SafeTool

// Approver is the human decision for a non-safe call (the daemon's confirm
// plugs in here).
type Approver = gate.Approver

// WithApprover installs the human approver for dangerous calls. Without it the
// gate is fail-closed (non-safe tools are denied).
func WithApprover(a gate.Approver) Option {
	return func(_ *core.Config, s *newState) { s.approver = a }
}

// WithToolGate installs a fully custom gate, bypassing the default policy.
func WithToolGate(g ac.ToolGate) Option {
	return func(c *core.Config, _ *newState) { c.Gate = g }
}

// AllowAll is the explicit, greppable opt-out from the fail-closed default.
func AllowAll() Option {
	return func(_ *core.Config, s *newState) { s.allowAll = true }
}
