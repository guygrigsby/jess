package jess

import (
	"strings"
	"testing"

	"github.com/guygrigsby/jess/model"
)

// TestLiteLLM_ReturnsModel verifies that LiteLLM wires through NewLiteLLMModel
// and returns a model.Model. The litellm openai provider requires an API key at
// construction time (no env-var fallback). Since this test must be network-free,
// it accepts a clean API-key error as proof that the constructor path works end
// to end. If the calling environment happens to inject a real key, the test
// instead asserts the returned value is a non-nil model.Model.
func TestLiteLLM_ReturnsModel(t *testing.T) {
	m, err := LiteLLM("openai", "gpt-4o")
	if err != nil {
		// Construction failure must be a recognizable validator error, not a
		// panic or an unexpected internal failure.
		if !strings.Contains(err.Error(), "API key") {
			t.Fatalf("LiteLLM: unexpected error (want API-key validation error): %v", err)
		}
		t.Logf("LiteLLM returned expected construction error (no API key): %v", err)
		return
	}
	var _ model.Model = m
	if m == nil {
		t.Fatal("expected a non-nil model.Model")
	}
}
