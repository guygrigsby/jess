package jess

import (
	"context"
	"errors"

	"github.com/guygrigsby/jess/internal/acl"
)

// Session is one conversation with an Agent: it holds the message history and
// runs one Prompt/Continue cycle at a time. Open multiple Sessions on one Agent
// for parallel conversations.
type Session struct {
	rt *acl.Runtime
}

// newSession builds a Session from the Agent's config.
func (a *Agent) newSession() (*Session, error) {
	rt, err := acl.NewRuntime(a.cfg)
	if err != nil {
		return nil, err
	}
	return &Session{rt: rt}, nil
}

// NewSession opens a new conversation Session on the Agent.
func (a *Agent) NewSession() (*Session, error) { return a.newSession() }

// Prompt starts a run with the given input. Returns ErrRunInProgress if a run
// is already active on this Session.
func (s *Session) Prompt(ctx context.Context, input string) (*Run, error) {
	r, err := s.rt.Prompt(ctx, input)
	return wrapRun(r, err)
}

// Continue resumes the conversation without new input.
func (s *Session) Continue(ctx context.Context) (*Run, error) {
	r, err := s.rt.Continue(ctx)
	return wrapRun(r, err)
}

func wrapRun(r *acl.Run, err error) (*Run, error) {
	if err != nil {
		if errors.Is(err, acl.ErrRunInProgress) {
			return nil, ErrRunInProgress
		}
		return nil, err
	}
	return &Run{inner: r}, nil
}
