package jess

import (
	"context"
	"errors"
	"sync"

	"github.com/guygrigsby/jess/internal/acl"
)

// Agent is the aggregate the host configures once: identity and capabilities
// (model, skills, tools, system prompt, and the AgentID that scopes memory).
// Safe for concurrent use; can back many Sessions. Memory belongs to the Agent
// and persists across conversations; message history belongs to each Session.
//
// Construct an Agent with New; the zero value is not usable.
type Agent struct {
	cfg acl.Config

	mu      sync.Mutex
	defSess *Session
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

func (a *Agent) defaultSession() (*Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.defSess == nil {
		s, err := a.newSession()
		if err != nil {
			return nil, err
		}
		a.defSess = s
	}
	return a.defSess, nil
}

// Prompt starts a run on the Agent's default Session.
func (a *Agent) Prompt(ctx context.Context, input string) (*Run, error) {
	s, err := a.defaultSession()
	if err != nil {
		return nil, err
	}
	return s.Prompt(ctx, input)
}

// Continue resumes the Agent's default Session.
func (a *Agent) Continue(ctx context.Context) (*Run, error) {
	s, err := a.defaultSession()
	if err != nil {
		return nil, err
	}
	return s.Continue(ctx)
}
