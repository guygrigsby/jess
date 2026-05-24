package gomlx

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/gomlx/go-huggingface/hub"
	"github.com/gomlx/gomlx/backends"
	_ "github.com/gomlx/gomlx/backends/simplego" // register the pure-Go "go" backend
	. "github.com/gomlx/gomlx/pkg/core/graph"
	"github.com/gomlx/gomlx/pkg/core/tensors"
	mlcontext "github.com/gomlx/gomlx/pkg/ml/context"
	onnxparser "github.com/gomlx/onnx-gomlx/onnx/parser"
)

// Default model + dims for the convenience constructor.
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

// Options configures NewEmbedder. Zero-value is fine and resolves
// to the all-MiniLM-L6-v2 defaults.
type Options struct {
	// ModelID is the HuggingFace repo (e.g.
	// "sentence-transformers/all-MiniLM-L6-v2"). Empty uses
	// DefaultModelID.
	ModelID string
	// Dim is the embedding dimensionality the model produces.
	// Empty uses DefaultDim (384 for MiniLM); set to match your
	// chosen model (768 for nomic-embed-text, 1024 for mxbai-large).
	Dim int
	// SeqLen is the fixed token length the model graph is built
	// for. Empty uses DefaultSeqLen (128). Longer texts are
	// truncated; shorter are padded.
	SeqLen int
	// AuthToken is the HuggingFace token (HF_TOKEN env). Required
	// only for gated repos; public models load without it.
	AuthToken string
}

// NewEmbedder downloads + loads a sentence-transformers model and
// returns an in-process Embedder ready to call.
func NewEmbedder(opts Options) (*Embedder, error) {
	if opts.ModelID == "" {
		opts.ModelID = DefaultModelID
	}
	if opts.Dim == 0 {
		opts.Dim = DefaultDim
	}
	if opts.SeqLen == 0 {
		opts.SeqLen = DefaultSeqLen
	}
	if opts.AuthToken == "" {
		opts.AuthToken = os.Getenv("HF_TOKEN")
	}

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

	// Explicitly request the pure-Go "go" backend (simplego). This
	// ignores $GOMLX_BACKEND because the whole point of this
	// embedder is no-CGO portability — letting an env var swap us
	// onto XLA at runtime would silently break that promise.
	backend, err := backends.NewWithConfig("go")
	if err != nil {
		return nil, fmt.Errorf("jess/memory/embed/gomlx: backend init (simplego): %w", err)
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
