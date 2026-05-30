package jess

import (
	"github.com/guygrigsby/jess/internal/acl"
	"github.com/guygrigsby/jess/model"
)

// LiteLLMConfig holds litellm model construction settings. Build it with the
// WithLLM* options passed to LiteLLM rather than populating it directly.
type LiteLLMConfig struct {
	APIKey    string
	BaseURL   string
	MaxTokens int
}

// LiteLLMOption configures a LiteLLM model at construction. Obtain from the
// WithLLM* constructors.
type LiteLLMOption func(*LiteLLMConfig)

// WithLLMAPIKey sets the provider API key.
func WithLLMAPIKey(key string) LiteLLMOption {
	return func(c *LiteLLMConfig) { c.APIKey = key }
}

// WithLLMBaseURL overrides the provider base URL (for a local OpenAI-compatible
// server or a gateway).
func WithLLMBaseURL(url string) LiteLLMOption {
	return func(c *LiteLLMConfig) { c.BaseURL = url }
}

// WithLLMMaxTokens caps the model's max output tokens per call (0 = provider
// default). Prevents over-long generations and provider 400s. Negative values
// are clamped to 0 (the cap is otherwise applied only when MaxTokens > 0, so a
// negative would silently no-op — likely a caller bug).
func WithLLMMaxTokens(n int) LiteLLMOption {
	return func(c *LiteLLMConfig) {
		if n < 0 {
			n = 0
		}
		c.MaxTokens = n
	}
}

// LiteLLM builds a cloud model backed by agentcore's litellm adapter and returns
// it as a vendor-free model.Model, suitable for jess.WithModel. provider and
// modelID are litellm identifiers, e.g. LiteLLM("openai","gpt-4o"). For a local
// or custom model, implement model.Model directly (or use model.Once).
//
// The agentcore dependency stays inside internal/acl; this package does not
// import the harness.
func LiteLLM(provider, modelID string, opts ...LiteLLMOption) (model.Model, error) {
	var cfg LiteLLMConfig
	for _, o := range opts {
		o(&cfg)
	}
	return acl.NewLiteLLMModel(provider, modelID, acl.LiteLLMConfig{
		APIKey:    cfg.APIKey,
		BaseURL:   cfg.BaseURL,
		MaxTokens: cfg.MaxTokens,
	})
}
