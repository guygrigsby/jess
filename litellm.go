package jess

import (
	"github.com/guygrigsby/jess/internal/acl"
	"github.com/guygrigsby/jess/model"
)

// LiteLLM builds a cloud model backed by agentcore's litellm adapter and
// returns it as a vendor-free model.Model, suitable for jess.WithModel.
// provider and modelID are litellm identifiers, e.g. LiteLLM("openai","gpt-4o").
// This is a convenience for cloud providers; for a local or custom model,
// implement model.Model directly (or use model.Once).
//
// The agentcore dependency stays inside internal/acl; this package does not
// import the harness.
func LiteLLM(provider, modelID string) (model.Model, error) {
	return acl.NewLiteLLMModel(provider, modelID)
}
