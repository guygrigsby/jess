package gomlx

import (
	"strings"
	"testing"
)

func TestResolveModelOptions_ModelWins(t *testing.T) {
	id, dim, seq, err := resolveModelOptions(Options{
		Model: ModelNomicEmbedText_V1_5,
		// These should be IGNORED — Model has precedence.
		ModelID: "should/not/win",
		Dim:     999,
		SeqLen:  9999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != ModelNomicEmbedText_V1_5.ID {
		t.Errorf("ID = %q, want %q (Model wins)", id, ModelNomicEmbedText_V1_5.ID)
	}
	if dim != ModelNomicEmbedText_V1_5.Dim {
		t.Errorf("Dim = %d, want %d", dim, ModelNomicEmbedText_V1_5.Dim)
	}
	if seq != ModelNomicEmbedText_V1_5.SeqLen {
		t.Errorf("SeqLen = %d, want %d", seq, ModelNomicEmbedText_V1_5.SeqLen)
	}
}

func TestResolveModelOptions_EmptyUsesDefault(t *testing.T) {
	id, dim, seq, err := resolveModelOptions(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if id != DefaultModel.ID || dim != DefaultModel.Dim || seq != DefaultModel.SeqLen {
		t.Errorf("empty Options should resolve to DefaultModel; got (%q, %d, %d)", id, dim, seq)
	}
}

func TestResolveModelOptions_ExplicitOverride(t *testing.T) {
	// All three set — use them verbatim, no auto-detect download.
	id, dim, seq, err := resolveModelOptions(Options{
		ModelID: "some/custom-model",
		Dim:     512,
		SeqLen:  256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "some/custom-model" || dim != 512 || seq != 256 {
		t.Errorf("explicit values not preserved: got (%q, %d, %d)", id, dim, seq)
	}
}

func TestResolveModelOptions_InvalidBundledModel(t *testing.T) {
	// Hand-built Model with missing dims — should error rather
	// than silently use zeros.
	_, _, _, err := resolveModelOptions(Options{
		Model: Model{ID: "x", Dim: 0, SeqLen: 0},
	})
	if err == nil {
		t.Fatal("zero Dim/SeqLen in bundled Model should error")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention 'invalid': %v", err)
	}
}

func TestParseModelConfig_StandardBERTFields(t *testing.T) {
	// Real all-MiniLM-L6-v2 config.json subset.
	raw := []byte(`{
		"hidden_size": 384,
		"max_position_embeddings": 512,
		"num_attention_heads": 12,
		"vocab_size": 30522
	}`)
	dim, seq, err := parseModelConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if dim != 384 {
		t.Errorf("Dim = %d, want 384", dim)
	}
	if seq != 512 {
		t.Errorf("SeqLen = %d, want 512", seq)
	}
}

func TestParseModelConfig_MissingHiddenSize(t *testing.T) {
	// Some non-BERT models use d_model instead of hidden_size.
	// We don't try to handle every variant — error clearly and
	// tell the caller to pass Dim explicitly.
	raw := []byte(`{"d_model": 768, "max_position_embeddings": 512}`)
	_, _, err := parseModelConfig(raw)
	if err == nil {
		t.Fatal("missing hidden_size should error")
	}
	if !strings.Contains(err.Error(), "hidden_size") {
		t.Errorf("error should mention hidden_size: %v", err)
	}
}

func TestParseModelConfig_MissingMaxPosition(t *testing.T) {
	raw := []byte(`{"hidden_size": 384}`)
	_, _, err := parseModelConfig(raw)
	if err == nil {
		t.Fatal("missing max_position_embeddings should error")
	}
	if !strings.Contains(err.Error(), "max_position_embeddings") {
		t.Errorf("error should mention the missing field: %v", err)
	}
}

func TestParseModelConfig_InvalidJSON(t *testing.T) {
	_, _, err := parseModelConfig([]byte("not json"))
	if err == nil {
		t.Fatal("malformed JSON should error")
	}
}

// Confirm the known-good constants all have plausible shapes.
// Doesn't validate that HF actually serves them — that would
// need network. Catches typos: Dim=0, SeqLen too small to be
// useful, empty ID.
func TestModelConstants_ShapeSanity(t *testing.T) {
	cases := []struct {
		name string
		m    Model
	}{
		{"MiniLM_L6_V2", ModelMiniLM_L6_V2},
		{"MiniLM_L12_V2", ModelMiniLM_L12_V2},
		{"MpNetBase_V2", ModelMpNetBase_V2},
		{"BGESmall_EN_V1_5", ModelBGESmall_EN_V1_5},
		{"BGEBase_EN_V1_5", ModelBGEBase_EN_V1_5},
		{"BGELarge_EN_V1_5", ModelBGELarge_EN_V1_5},
		{"E5Small_V2", ModelE5Small_V2},
		{"E5Base_V2", ModelE5Base_V2},
		{"NomicEmbedText_V1_5", ModelNomicEmbedText_V1_5},
		{"MxbaiEmbedLarge_V1", ModelMxbaiEmbedLarge_V1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.m.ID == "" {
				t.Error("ID empty")
			}
			if !strings.Contains(c.m.ID, "/") {
				t.Errorf("ID %q missing org/repo separator", c.m.ID)
			}
			if c.m.Dim <= 0 {
				t.Errorf("Dim = %d, not plausible", c.m.Dim)
			}
			if c.m.SeqLen < 64 {
				t.Errorf("SeqLen = %d, too small to be useful", c.m.SeqLen)
			}
		})
	}
}
