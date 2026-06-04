package subagent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTool_RunsSubagentAndReturnsOutput(t *testing.T) {
	p := New(WithMaxConcurrent(2))
	p.Register(echo("research", "found it"))
	tl := NewTool(p)

	if tl.Name() != "subagent" {
		t.Fatalf("Name = %q", tl.Name())
	}

	// The subagent's events flow onto the pool's merged stream, AgentPath-tagged.
	done := make(chan struct{})
	var sawTagged bool
	go func() {
		for ev := range p.Events() {
			if len(ev.AgentPath) > 0 {
				sawTagged = true
			}
		}
		close(done)
	}()

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"agent":"research","task":"dig"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	p.Close()
	<-done

	if !sawTagged {
		t.Error("subagent events were not tagged on the pool stream")
	}
	var resp struct {
		Agent  string `json:"agent"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
	if resp.Agent != "research" || resp.Output != "found it" {
		t.Errorf("result = %+v", resp)
	}
}

func TestTool_UnknownAgentError(t *testing.T) {
	tl := NewTool(New())
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"agent":"nope","task":"x"}`)); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}
