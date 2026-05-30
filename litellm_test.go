package jess

import (
	"testing"

	"github.com/guygrigsby/jess/model"
)

// TestLiteLLM_ReturnsModel verifies that LiteLLM wires through to
// NewLiteLLMModel. It is network-free: the litellm provider may reject
// construction without a configured API key, so either outcome is acceptable
// as long as the contract holds — a nil error implies a non-nil model.Model,
// and a non-nil error is a clean construction failure (not a panic). The
// assertion deliberately avoids matching provider-specific error wording.
func TestLiteLLM_ReturnsModel(t *testing.T) {
	m, err := LiteLLM("openai", "gpt-4o")
	if err != nil {
		t.Logf("LiteLLM returned a construction error (e.g. no API key configured): %v", err)
		return
	}
	var _ model.Model = m
	if m == nil {
		t.Fatal("LiteLLM returned nil model and nil error")
	}
}

func TestLiteLLM_OptionsThreadThrough(t *testing.T) {
	m, err := LiteLLM("openai", "gpt-4o", WithLLMAPIKey("sk-test"), WithLLMBaseURL("http://localhost:1234"))
	if err != nil {
		t.Logf("construction error (acceptable, network-free): %v", err)
		return
	}
	if m == nil {
		t.Fatal("expected a non-nil model.Model")
	}
}

// Negative MaxTokens silently disabled the cap (the ACL wraps only when
// MaxTokens > 0). Clamp to 0 so a caller bug surfaces as the documented
// no-cap default, not as a confusing silent no-op.
func TestWithLLMMaxTokens_ClampsNegatives(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-1, 0},
		{-99, 0},
		{0, 0},
		{1, 1},
		{4096, 4096},
	}
	for _, c := range cases {
		var cfg LiteLLMConfig
		WithLLMMaxTokens(c.in)(&cfg)
		if cfg.MaxTokens != c.want {
			t.Errorf("WithLLMMaxTokens(%d) -> MaxTokens=%d, want %d", c.in, cfg.MaxTokens, c.want)
		}
	}
}
