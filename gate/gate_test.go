package gate

import (
	"context"
	"encoding/json"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
)

type safeTool struct{}

func (safeTool) Name() string          { return "list_services" }
func (safeTool) Description() string    { return "" }
func (safeTool) Schema() map[string]any { return map[string]any{} }
func (safeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}
func (safeTool) Safe() bool { return true }

type dangerTool struct{ safeTool }

func (dangerTool) Name() string { return "restart_service" }
func (dangerTool) Safe() bool   { return false }

type recSink struct{ events []ledger.Event }

func (r *recSink) Record(e ledger.Event) error { r.events = append(r.events, e); return nil }

func req(t ac.Tool) ac.GateRequest {
	return ac.GateRequest{Tool: t, Call: ac.ToolCall{Name: t.Name()}}
}

func TestSafeToolAllowedNoApprover(t *testing.T) {
	rs := &recSink{}
	g := New(Policy{Audit: rs})
	d, err := g(context.Background(), req(safeTool{}))
	if err != nil || d == nil || !d.Allowed {
		t.Fatalf("safe tool should be allowed: d=%+v err=%v", d, err)
	}
}

func TestUnsafeToolDeniedWhenNoApprover(t *testing.T) {
	rs := &recSink{}
	g := New(Policy{Audit: rs})
	d, _ := g(context.Background(), req(dangerTool{}))
	if d == nil || d.Allowed {
		t.Fatalf("unsafe tool must be denied fail-closed, got %+v", d)
	}
	var sawDenied bool
	for _, e := range rs.events {
		if e.Kind == ledger.KindGateDecision && e.Verdict == ledger.VerdictDenied {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Fatalf("denied call not recorded to audit: %+v", rs.events)
	}
}

func TestApproverRoutesUnsafe(t *testing.T) {
	rs := &recSink{}
	approved := Approver(func(context.Context, Request) (bool, string) { return true, "ok" })
	g := New(Policy{Audit: rs, Approver: approved})
	d, _ := g(context.Background(), req(dangerTool{}))
	if d == nil || !d.Allowed {
		t.Fatalf("approver said yes, expected allowed: %+v", d)
	}
}

func TestAllowAllPermits(t *testing.T) {
	d, err := AllowAll()(context.Background(), req(dangerTool{}))
	if err != nil || d == nil || !d.Allowed {
		t.Fatalf("AllowAll must permit: %+v %v", d, err)
	}
}

func TestDangerous(t *testing.T) {
	if !Dangerous("sudo rm -rf /") {
		t.Fatal("rm -rf should be dangerous")
	}
	if Dangerous("ls -la /tmp") {
		t.Fatal("ls should be safe")
	}
}
