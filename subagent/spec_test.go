package subagent

import (
	"testing"

	"github.com/guygrigsby/jess/internal/core"
)

func TestSpec_Config(t *testing.T) {
	m := core.Once(false, nil) // fn unused; mapping only
	s := Spec{Name: "research", Model: m, SystemPrompt: "be brief", AgentID: "research", MaxTurns: 4}
	cfg := s.config(core.Config{})
	if cfg.Model == nil || cfg.SystemPrompt != "be brief" || cfg.AgentID != "research" || cfg.MaxTurns != 4 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestSpec_ConfigInheritsBase(t *testing.T) {
	base := core.Config{
		Model:   core.Once(false, nil),
		AgentID: "parent",
	}
	// Spec with no model / no agentID inherits from base.
	s := Spec{Name: "child"}
	cfg := s.config(base)
	if cfg.Model == nil {
		t.Fatal("expected inherited model from base")
	}
	if cfg.AgentID != "parent" {
		t.Fatalf("AgentID = %q, want inherited %q", cfg.AgentID, "parent")
	}
}

func TestTask_AgentPathReturnsCopy(t *testing.T) {
	tk := &Task{agentPath: []string{"research/0001"}}
	got := tk.AgentPath()
	got[0] = "mutated"
	if tk.AgentPath()[0] != "research/0001" {
		t.Fatal("AgentPath must return a copy; caller mutation leaked into the task")
	}
}

func TestTask_WaitResultAgentPathIsCopy(t *testing.T) {
	tk := &Task{agentPath: []string{"research/0001"}, done: make(chan struct{})}
	tk.res = Result{AgentPath: tk.agentPath}
	close(tk.done)

	r1, _ := tk.Wait()
	r1.AgentPath[0] = "mutated"

	r2, _ := tk.Wait()
	if r2.AgentPath[0] != "research/0001" {
		t.Fatalf("Wait must return a fresh AgentPath copy; caller mutation leaked (got %q)", r2.AgentPath[0])
	}
}
