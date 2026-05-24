package gomlx

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/gomlx/go-huggingface/hub"
)

// resolveModelOptions applies the precedence rules documented on
// Options to produce a concrete (modelID, dim, seqLen) tuple.
//
// 1. opts.Model wins (bundled triple, known-good)
// 2. opts.ModelID + opts.Dim + opts.SeqLen all set (explicit override)
// 3. opts.ModelID set, Dim/SeqLen zero (auto-detect from config.json)
// 4. nothing set → DefaultModel (MiniLM-L6-v2)
//
// Pulled into its own function so the precedence logic is testable
// without standing up an Embedder.
func resolveModelOptions(opts Options) (modelID string, dim, seqLen int, err error) {
	// Rule 1: bundled Model wins.
	if opts.Model.ID != "" {
		if opts.Model.Dim <= 0 || opts.Model.SeqLen <= 0 {
			return "", 0, 0, fmt.Errorf("jess/memory/embed/gomlx: Options.Model %q has invalid Dim=%d SeqLen=%d", opts.Model.ID, opts.Model.Dim, opts.Model.SeqLen)
		}
		return opts.Model.ID, opts.Model.Dim, opts.Model.SeqLen, nil
	}

	// Rule 4: nothing set → DefaultModel.
	if opts.ModelID == "" {
		return DefaultModel.ID, DefaultModel.Dim, DefaultModel.SeqLen, nil
	}

	// Rule 2: explicit override.
	if opts.Dim > 0 && opts.SeqLen > 0 {
		return opts.ModelID, opts.Dim, opts.SeqLen, nil
	}

	// Rule 3: auto-detect from config.json.
	dim, seqLen, err = autoDetectModelShape(opts.ModelID, opts.AuthToken)
	if err != nil {
		return "", 0, 0, fmt.Errorf("jess/memory/embed/gomlx: auto-detect for %q failed (pass Dim+SeqLen explicitly to skip detection): %w", opts.ModelID, err)
	}
	return opts.ModelID, dim, seqLen, nil
}

// autoDetectModelShape downloads config.json for the given HF repo
// and parses the standard BERT-family fields:
//
//	hidden_size                  → Dim
//	max_position_embeddings      → SeqLen
//
// Both fields are required; missing either produces a clear error
// so the caller can fall back to explicit Dim+SeqLen instead of
// silently picking wrong values.
func autoDetectModelShape(modelID, authToken string) (dim, seqLen int, err error) {
	if authToken == "" {
		authToken = os.Getenv("HF_TOKEN")
	}
	repo := hub.New(modelID).WithAuth(authToken)
	path, err := repo.DownloadFile("config.json")
	if err != nil {
		return 0, 0, fmt.Errorf("download config.json: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", path, err)
	}
	return parseModelConfig(raw)
}

// modelConfig is the minimal subset of HuggingFace's config.json
// that we need. Real configs have dozens of fields; we ignore
// everything else.
type modelConfig struct {
	HiddenSize            int `json:"hidden_size"`
	MaxPositionEmbeddings int `json:"max_position_embeddings"`
}

// parseModelConfig is split out from autoDetectModelShape so unit
// tests can exercise the parse logic without a network round-trip.
func parseModelConfig(raw []byte) (dim, seqLen int, err error) {
	var cfg modelConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 0, 0, fmt.Errorf("parse config.json: %w", err)
	}
	if cfg.HiddenSize <= 0 {
		return 0, 0, fmt.Errorf("config.json missing or zero hidden_size (model may use a non-BERT key like d_model; pass Dim explicitly)")
	}
	if cfg.MaxPositionEmbeddings <= 0 {
		return 0, 0, fmt.Errorf("config.json missing or zero max_position_embeddings (pass SeqLen explicitly)")
	}
	return cfg.HiddenSize, cfg.MaxPositionEmbeddings, nil
}
