package jess

import (
	"errors"

	"github.com/guygrigsby/jess/internal/acl"
)

// Agent is the aggregate the host configures once: identity and capabilities
// (model, skills, tools, system prompt, and the AgentID that scopes memory).
// Safe for concurrent use; can back many Sessions. Memory belongs to the Agent
// and persists across conversations; message history belongs to each Session.
type Agent struct {
	cfg acl.Config
}

// New builds an Agent from options. WithModel is required.
func New(opts ...Option) (*Agent, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.model == nil {
		return nil, errors.New("jess: WithModel is required")
	}
	return &Agent{cfg: acl.Config{
		Model:        o.model,
		Tools:        o.tools,
		Skills:       o.skills,
		SystemPrompt: o.systemPrompt,
		Store:        o.store,
		Recaller:     o.recaller,
		AgentID:      o.agentID,
		MaxTurns:     o.maxTurns,
	}}, nil
}
