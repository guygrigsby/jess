// Package gate is jess's preventive control: a fail-closed tool gate. Omitting
// an approver does not mean allow-all; it means deny anything not declared
// safe. Permissiveness is opt-in (AllowAll) and greppable.
package gate

import (
	"context"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
)

// SafeTool is the optional marker a tool implements to be auto-approved. Safe
// means read-only or bounded: its effect is obvious by construction.
type SafeTool interface{ Safe() bool }

// Request is what an Approver sees for one non-safe call.
type Request struct {
	Tool    string
	Label   string
	Preview string
	Args    []byte
}

// Approver decides one non-safe call. Return (true, reason) to allow.
type Approver func(ctx context.Context, r Request) (allow bool, reason string)

// Policy configures the default gate.
type Policy struct {
	Approver  Approver
	Audit     ledger.Sink
	AgentPath string
}

// New builds an agentcore.ToolGate from the policy.
func New(p Policy) ac.ToolGate {
	return func(ctx context.Context, gr ac.GateRequest) (*ac.GateDecision, error) {
		safe := false
		if st, ok := gr.Tool.(SafeTool); ok {
			safe = st.Safe()
		}
		rec := func(v ledger.Verdict, reason string) {
			if p.Audit != nil {
				_ = p.Audit.Record(ledger.Event{
					AgentPath: p.AgentPath,
					Kind:      ledger.KindGateDecision,
					Tool:      gr.Call.Name,
					Label:     gr.ToolLabel,
					Preview:   string(gr.Preview),
					Args:      gr.Call.Args,
					Verdict:   v,
					Reason:    reason,
				})
			}
		}
		if safe {
			rec(ledger.VerdictAllowed, "safe tool")
			return &ac.GateDecision{Allowed: true}, nil
		}
		if p.Approver == nil {
			rec(ledger.VerdictDenied, "no approver; fail-closed")
			return &ac.GateDecision{Allowed: false, Reason: "denied: no approver configured for non-safe tool"}, nil
		}
		allow, reason := p.Approver(ctx, Request{
			Tool: gr.Call.Name, Label: gr.ToolLabel, Preview: string(gr.Preview), Args: gr.Call.Args,
		})
		if allow {
			rec(ledger.VerdictAllowed, reason)
			return &ac.GateDecision{Allowed: true}, nil
		}
		rec(ledger.VerdictDenied, reason)
		return &ac.GateDecision{Allowed: false, Reason: "denied by approver: " + reason}, nil
	}
}

// AllowAll is the explicit, greppable opt-out: a gate that permits everything.
func AllowAll() ac.ToolGate {
	return func(context.Context, ac.GateRequest) (*ac.GateDecision, error) {
		return &ac.GateDecision{Allowed: true}, nil
	}
}
