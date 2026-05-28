package subagent

import (
	"testing"

	"github.com/guygrigsby/jess/model"
)

func TestSpec_Config(t *testing.T) {
	m := model.Once(false, nil) // fn unused; mapping only
	s := Spec{Name: "research", Model: m, SystemPrompt: "be brief", AgentID: "research", MaxTurns: 4}
	cfg := s.config()
	if cfg.Model == nil || cfg.SystemPrompt != "be brief" || cfg.AgentID != "research" || cfg.MaxTurns != 4 {
		t.Fatalf("config = %+v", cfg)
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
