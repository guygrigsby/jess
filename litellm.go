package jess

import (
	"github.com/guygrigsby/jess/internal/acl"
	"github.com/guygrigsby/jess/model"
)

// LiteLLMOption configures a LiteLLM model at construction. Obtain from the
// WithLLM* constructors.
type LiteLLMOption func(*acl.LiteLLMConfig)

// WithLLMAPIKey sets the provider API key.
func WithLLMAPIKey(key string) LiteLLMOption {
	return func(c *acl.LiteLLMConfig) { c.APIKey = key }
}

// WithLLMBaseURL overrides the provider base URL (for a local OpenAI-compatible
// server or a gateway).
func WithLLMBaseURL(url string) LiteLLMOption {
	return func(c *acl.LiteLLMConfig) { c.BaseURL = url }
}

// LiteLLM builds a cloud model backed by agentcore's litellm adapter and returns
// it as a vendor-free model.Model, suitable for jess.WithModel. provider and
// modelID are litellm identifiers, e.g. LiteLLM("openai","gpt-4o"). For a local
// or custom model, implement model.Model directly (or use model.Once).
//
// The agentcore dependency stays inside internal/acl; this package does not
// import the harness.
func LiteLLM(provider, modelID string, opts ...LiteLLMOption) (model.Model, error) {
	var cfg acl.LiteLLMConfig
	for _, o := range opts {
		o(&cfg)
	}
	return acl.NewLiteLLMModel(provider, modelID, cfg)
}
