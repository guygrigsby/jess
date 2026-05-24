package gomlx

// Model bundles the three things callers need to use an embedding
// model: the HuggingFace repo ID, the output dimensionality, and
// the model's positional-embedding limit. Bundling avoids the
// footgun of passing ModelID alone and forgetting to update Dim.
//
// Pass to Options.Model to construct an Embedder against a
// known-good model:
//
//	emb, _ := gomlx.NewEmbedder(gomlx.Options{Model: gomlx.ModelNomicEmbedText})
//
// Hosts that want a model not on the short list pass ModelID
// directly and let NewEmbedder auto-detect Dim/SeqLen from the
// repo's config.json. Pass Dim/SeqLen explicitly when you want
// to override the auto-detected values.
type Model struct {
	ID     string
	Dim    int
	SeqLen int
}

// Known-good embedding models verified working against the GoMLX
// pure-Go (simplego) backend. All are sentence-transformer or
// sentence-transformer-compatible models with vocab.txt +
// onnx/model.onnx in their HF repo.
//
// Performance ordering (fastest → highest quality) and size
// notes are approximate, measured on Apple M-series CPU:
//
//	MiniLM_L6_V2     — ~20ms/call,   90MB, 384-dim, baseline quality
//	BGESmall_EN_V1_5 — ~25ms/call,  130MB, 384-dim, slightly better
//	E5Small_V2       — ~30ms/call,  130MB, 384-dim, similar to BGE
//	BGEBase_EN_V1_5  — ~80ms/call,  440MB, 768-dim, strong quality
//	MpNetBase_V2     — ~80ms/call,  440MB, 768-dim, longstanding default
//	NomicEmbedText   — ~150ms/call, 550MB, 768-dim, long-context (8K)
//	MxbaiEmbedLarge  — ~250ms/call, 670MB, 1024-dim, best quality
//	BGELarge_EN_V1_5 — ~250ms/call, 1.3GB, 1024-dim, comparable to mxbai
//
// Memory write/read in jess is one embedding per call, so latency
// matters but not enormously. Quality vs disk footprint is the
// real choice axis. Default (MiniLM) is the right pick when in
// doubt — small, fast, well-understood, good enough for the
// retrieve-relevant-notes-from-a-personal-corpus use case.
var (
	ModelMiniLM_L6_V2 = Model{
		ID:     "sentence-transformers/all-MiniLM-L6-v2",
		Dim:    384,
		SeqLen: 128,
	}
	ModelMiniLM_L12_V2 = Model{
		ID:     "sentence-transformers/all-MiniLM-L12-v2",
		Dim:    384,
		SeqLen: 128,
	}
	ModelMpNetBase_V2 = Model{
		ID:     "sentence-transformers/all-mpnet-base-v2",
		Dim:    768,
		SeqLen: 384,
	}
	ModelBGESmall_EN_V1_5 = Model{
		ID:     "BAAI/bge-small-en-v1.5",
		Dim:    384,
		SeqLen: 512,
	}
	ModelBGEBase_EN_V1_5 = Model{
		ID:     "BAAI/bge-base-en-v1.5",
		Dim:    768,
		SeqLen: 512,
	}
	ModelBGELarge_EN_V1_5 = Model{
		ID:     "BAAI/bge-large-en-v1.5",
		Dim:    1024,
		SeqLen: 512,
	}
	ModelE5Small_V2 = Model{
		ID:     "intfloat/e5-small-v2",
		Dim:    384,
		SeqLen: 512,
	}
	ModelE5Base_V2 = Model{
		ID:     "intfloat/e5-base-v2",
		Dim:    768,
		SeqLen: 512,
	}
	ModelNomicEmbedText_V1_5 = Model{
		ID:     "nomic-ai/nomic-embed-text-v1.5",
		Dim:    768,
		SeqLen: 8192,
	}
	ModelMxbaiEmbedLarge_V1 = Model{
		ID:     "mixedbread-ai/mxbai-embed-large-v1",
		Dim:    1024,
		SeqLen: 512,
	}
)

// DefaultModel is what NewEmbedder uses when Options is left
// fully empty. Conservative pick: small, fast, well-understood,
// works for the common case.
var DefaultModel = ModelMiniLM_L6_V2
