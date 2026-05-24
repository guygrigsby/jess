package gomlx

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
)

// TestEmbedder_EndToEnd downloads the actual all-MiniLM-L6-v2 model
// (~90MB) on first run, then exercises the full pipeline: tokenize
// → ONNX inference via the simplego pure-Go backend → mean-pool →
// L2 normalize → 384-dim []float32. Subsequent runs use HuggingFace's
// on-disk cache (~/.cache/huggingface).
//
// Skipped by default: gated on JESS_EMBEDDER_E2E=1 because the download
// + warm-up is ~5–15s and we don't want every `go test ./...` paying
// that. Set the env var when validating the GoMLX integration.
func TestEmbedder_EndToEnd(t *testing.T) {
	if os.Getenv("JESS_EMBEDDER_E2E") == "" {
		t.Skip("set JESS_EMBEDDER_E2E=1 to run (downloads ~90MB model)")
	}

	emb, err := NewEmbedder(Options{})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	// Single Embed roundtrip — confirms graph builds, model runs,
	// output is the expected shape.
	vec, err := emb.Embed(context.Background(), "Hello, world.")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != DefaultDim {
		t.Fatalf("vector len = %d, want %d", len(vec), DefaultDim)
	}

	// L2 norm of a unit vector is 1 ± float error. If pooling or
	// normalization is off we'd see something visibly wrong.
	var sumSq float32
	for _, x := range vec {
		sumSq += x * x
	}
	norm := math.Sqrt(float64(sumSq))
	if math.Abs(norm-1) > 1e-3 {
		t.Errorf("output should be L2-normalized, got norm = %v", norm)
	}

	// Sanity: not all zeros (would indicate model didn't run).
	allZero := true
	for _, x := range vec {
		if x != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("output is all zeros — model probably didn't run")
	}

	// Semantic sanity: two near-synonymous sentences should land
	// closer together than two unrelated ones. Weak guarantee
	// (model is not deterministic across architectures), but a
	// gross regression (e.g. all sentences embed identically)
	// would show up.
	related1, _ := emb.Embed(context.Background(), "The cat sat on the mat.")
	related2, _ := emb.Embed(context.Background(), "A feline rested on the rug.")
	unrelated, _ := emb.Embed(context.Background(), "Generics in Go 1.18 use type parameters.")
	if cosine(related1, related2) <= cosine(related1, unrelated) {
		t.Errorf("semantic ordering wrong: related=%v unrelated=%v",
			cosine(related1, related2), cosine(related1, unrelated))
	}
}

// TestEmbedder_DimAndName covers the cheap API surface without
// requiring the model. Constructs an Embedder by hand-rolling
// just enough to validate the trivial accessors, since New
// pulls a 90MB download.
func TestEmbedder_DimAndName(t *testing.T) {
	e := &Embedder{modelID: "sentence-transformers/all-MiniLM-L6-v2", dim: 384}
	if e.Dim() != 384 {
		t.Errorf("Dim() = %d, want 384", e.Dim())
	}
	if !strings.HasPrefix(e.Name(), "gomlx:") {
		t.Errorf("Name() = %q, should be prefixed gomlx:", e.Name())
	}
	if !strings.Contains(e.Name(), "MiniLM") {
		t.Errorf("Name() should embed the model ID, got %q", e.Name())
	}
}

func TestTokenizer_EncodeRoundtrip(t *testing.T) {
	// Minimal vocab to exercise the WordPiece flow without a real
	// model download. Includes the four special tokens + a couple
	// of test pieces.
	dir := t.TempDir()
	vocabPath := dir + "/vocab.txt"
	const vocab = "[PAD]\n[UNK]\n[CLS]\n[SEP]\nhello\n##world\nworld\n"
	if err := os.WriteFile(vocabPath, []byte(vocab), 0o644); err != nil {
		t.Fatal(err)
	}
	tok, err := newTokenizer(vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	ids := tok.encode("hello world")
	if len(ids) < 3 {
		t.Fatalf("encode returned %d ids, want at least 3 (CLS + ... + SEP)", len(ids))
	}
	if ids[0] != tok.clsID {
		t.Errorf("first token should be [CLS] (id=%d), got %d", tok.clsID, ids[0])
	}
	if ids[len(ids)-1] != tok.sepID {
		t.Errorf("last token should be [SEP] (id=%d), got %d", tok.sepID, ids[len(ids)-1])
	}
}

func TestTokenizer_PaddingAndTruncation(t *testing.T) {
	dir := t.TempDir()
	vocabPath := dir + "/vocab.txt"
	const vocab = "[PAD]\n[UNK]\n[CLS]\n[SEP]\nhi\n"
	_ = os.WriteFile(vocabPath, []byte(vocab), 0o644)
	tok, _ := newTokenizer(vocabPath)

	ids, mask, types := tok.encodeBatch([]string{"hi"}, 8)
	if len(ids) != 8 || len(mask) != 8 || len(types) != 8 {
		t.Fatalf("encodeBatch returned wrong lengths: %d %d %d", len(ids), len(mask), len(types))
	}
	// "hi" → [CLS] hi [SEP] = 3 tokens; rest is padding.
	for i := 3; i < 8; i++ {
		if mask[i] != 0 {
			t.Errorf("attention_mask[%d] should be 0 (padding), got %d", i, mask[i])
		}
		if ids[i] != int64(tok.padID) {
			t.Errorf("input_ids[%d] should be PAD (%d), got %d", i, tok.padID, ids[i])
		}
	}
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
