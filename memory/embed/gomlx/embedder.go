package gomlx

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/gomlx/compute"
	_ "github.com/gomlx/compute/gobackend" // register the pure-Go backend
	"github.com/gomlx/go-huggingface/hub"
	. "github.com/gomlx/gomlx/pkg/core/graph"
	"github.com/gomlx/gomlx/pkg/core/tensors"
	mlcontext "github.com/gomlx/gomlx/pkg/ml/context"
	onnxparser "github.com/gomlx/onnx-gomlx/onnx/parser"
)

// Legacy defaults kept for callers that referenced them directly.
// New code should prefer DefaultModel (models.go) — it bundles the
// three together and matches the same MiniLM target.
const (
	DefaultModelID = "sentence-transformers/all-MiniLM-L6-v2"
	DefaultDim     = 384
	DefaultSeqLen  = 128
)

// Embedder runs sentence-transformers BERT-style embedding models
// in-process via the pure-Go gomlx/compute/gobackend backend. No
// subprocess, no external runtime (no .so, no ONNX Runtime), no
// CGO — talon's single-binary cross-compile story stays intact.
//
// Construction downloads the model + vocab from HuggingFace on
// first use (cached under HF's standard location: $HF_HOME or
// ~/.cache/huggingface). Subsequent constructions are warm.
//
// Concurrency: Embed is safe to call from multiple goroutines.
// The internal exec is shared and protected by a mutex — running
// embeddings serially for now since gobackend's reentrancy isn't
// well-characterized and memory writes don't need parallelism.
type Embedder struct {
	modelID string
	dim     int
	seqLen  int

	tok    *tokenizer
	exec   *mlcontext.Exec
	mu     sync.Mutex
}

// Options configures NewEmbedder. Resolution order, highest
// precedence first:
//
//  1. Model (a bundled ID/Dim/SeqLen triple) — use one of the
//     known-good constants (ModelNomicEmbedText, etc.).
//  2. ModelID + Dim + SeqLen all set — explicit override.
//  3. ModelID set with Dim/SeqLen zero — auto-detect Dim/SeqLen
//     from the model's HuggingFace config.json (one extra ~5KB
//     download on first run; cached after).
//  4. Everything empty — falls back to DefaultModel (MiniLM-L6-v2).
//
// Resolution happens BEFORE the model download — invalid combinations
// surface as construction errors, not silent wrong-vector bugs.
type Options struct {
	// Model bundles ID/Dim/SeqLen as one value. Highest-precedence
	// way to specify the model. Use the canonical constants
	// (gomlx.ModelMiniLM_L6_V2, gomlx.ModelNomicEmbedText_V1_5,
	// etc.) for known-good targets, or build your own Model
	// literal for a HF repo not on the list.
	Model Model

	// ModelID is the HuggingFace repo (e.g.
	// "sentence-transformers/all-MiniLM-L6-v2"). Used when Model
	// is the zero value. When Dim/SeqLen below are zero,
	// NewEmbedder auto-detects them from the repo's config.json.
	ModelID string
	// Dim is the embedding dimensionality the model produces.
	// When zero (and Model is unset), auto-detected from
	// config.json's hidden_size.
	Dim int
	// SeqLen is the fixed token length the model graph is built
	// for. When zero (and Model is unset), auto-detected from
	// config.json's max_position_embeddings. Longer texts are
	// truncated; shorter are padded.
	SeqLen int
	// AuthToken is the HuggingFace token (HF_TOKEN env). Required
	// only for gated repos; public models load without it.
	AuthToken string
}

// NewEmbedder downloads + loads a sentence-transformers model and
// returns an in-process Embedder ready to call.
func NewEmbedder(opts Options) (*Embedder, error) {
	modelID, dim, seqLen, err := resolveModelOptions(opts)
	if err != nil {
		return nil, err
	}
	if opts.AuthToken == "" {
		opts.AuthToken = os.Getenv("HF_TOKEN")
	}
	opts.ModelID = modelID
	opts.Dim = dim
	opts.SeqLen = seqLen

	repo := hub.New(opts.ModelID).WithAuth(opts.AuthToken)
	vocabPath, err := repo.DownloadFile("vocab.txt")
	if err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: download vocab.txt from %s: %w", opts.ModelID, err)
	}
	onnxPath, err := repo.DownloadFile("onnx/model.onnx")
	if err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: download onnx/model.onnx from %s: %w", opts.ModelID, err)
	}

	tok, err := newTokenizer(vocabPath)
	if err != nil {
		return nil, err
	}
	onnxModel, err := onnxparser.ParseFile(onnxPath)
	if err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: parse ONNX %s: %w", onnxPath, err)
	}

	ctx := mlcontext.New()
	if err := onnxModel.VariablesToContext(ctx); err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: load weights: %w", err)
	}
	ctx = ctx.Reuse()

	// Anonymous import of compute/gobackend above registers the
	// pure-Go backend as the only candidate, so compute.New picks
	// it deterministically. Whole point of this embedder is no-CGO
	// portability; we don't want $GOMLX_BACKEND silently swapping
	// us onto an XLA build at runtime, so we don't import any
	// other backend here.
	backend, err := compute.New()
	if err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: backend init: %w", err)
	}

	// Build the inference graph once. Mean-pooling and L2
	// normalization happen INSIDE the graph so the per-Embed
	// hot path stays at the tensor-copy + run boundary.
	exec, err := mlcontext.NewExec(backend, ctx, func(ctx *mlcontext.Context, tokenIDs, attentionMask, tokenTypeIDs *Node) *Node {
		g := tokenIDs.Graph()
		outputs := onnxModel.CallGraph(ctx, g,
			map[string]*Node{
				"input_ids":      tokenIDs,
				"attention_mask": attentionMask,
				"token_type_ids": tokenTypeIDs,
			},
			"last_hidden_state")
		hidden := outputs[0] // shape: [batch, seqLen, dim]
		return meanPoolAndNormalize(hidden, attentionMask)
	})
	if err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: build exec: %w", err)
	}

	return &Embedder{
		modelID: opts.ModelID,
		dim:     opts.Dim,
		seqLen:  opts.SeqLen,
		tok:     tok,
		exec:    exec,
	}, nil
}

// Embed produces one vector per input text. Implements
// memory.Embedder.Embed for single-text calls; use EmbedBatch when
// you have multiple sentences to amortize the model call.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// EmbedBatch produces one vector per sentence. The model graph was
// built for a fixed batch dimension at construction time; mismatched
// batch sizes trigger a re-build (slow). For now batches are
// always 1 — talon's memory writes are one-at-a-time and recall
// queries one-at-a-time. Multi-batch support is a TODO.
func (e *Embedder) EmbedBatch(_ context.Context, sentences []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(sentences) != 1 {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: batch size %d not yet supported (only 1)", len(sentences))
	}

	idsFlat, maskFlat, typeFlat := e.tok.encodeBatch(sentences, e.seqLen)
	// Reshape flat slices into [batch=1][seqLen] for the graph.
	ids := [][]int64{idsFlat}
	mask := [][]int64{maskFlat}
	types := [][]int64{typeFlat}

	out := e.exec.MustExec(ids, mask, types)[0]
	flat := tensors.MustCopyFlatData[float32](out)
	if len(flat) != e.dim {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: expected %d floats, got %d", e.dim, len(flat))
	}
	return [][]float32{flat}, nil
}

// Dim returns the embedding dimensionality. Implements memory.Embedder.
func (e *Embedder) Dim() int { return e.dim }

// Name returns a stable identifier for this embedder, used by
// memory.Store to tag entries with the embedder that produced
// their vector. Implements memory.Embedder.
func (e *Embedder) Name() string { return "gomlx:" + e.modelID }

// meanPoolAndNormalize computes the sentence embedding from a
// BERT last_hidden_state output: take the masked mean across the
// sequence dim, then L2-normalize.
//
// Implemented as a graph fragment so it runs on the same backend
// (and gets the same SIMD treatment) as the rest of the model.
func meanPoolAndNormalize(hidden, attentionMask *Node) *Node {
	// Expand attentionMask to [batch, seqLen, 1] so it broadcasts
	// against the hidden dim, and convert to the same dtype as
	// hidden so the multiply works.
	maskF := ConvertDType(attentionMask, hidden.DType())
	maskExpanded := InsertAxes(maskF, -1) // [batch, seqLen, 1]
	masked := Mul(hidden, maskExpanded)

	// Sum over seqLen.
	sumHidden := ReduceSum(masked, 1)              // [batch, dim]
	sumMask := ReduceSum(maskExpanded, 1)          // [batch, 1]
	sumMaskClamped := MaxScalar(sumMask, 1e-9)     // avoid div-by-zero on all-pad
	mean := Div(sumHidden, sumMaskClamped)         // [batch, dim]

	// L2 normalize: divide by sqrt(sum(x^2)) per row, with a small
	// epsilon for numeric stability. ReduceSum collapses the last
	// axis; InsertAxes reinstates it so the broadcast Div works.
	squared := Mul(mean, mean)
	norm := Sqrt(MaxScalar(InsertAxes(ReduceSum(squared, -1), -1), 1e-12))
	return Div(mean, norm)
}
