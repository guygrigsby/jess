package memory

import (
	"context"
)

// Embedder turns text into a vector for nearest-neighbor retrieval.
// Implementations are expected to be safe for concurrent use across
// distinct Embed calls — the harness will batch embeddings for
// memory writes and re-call for query-time recall.
//
// Vectors returned by a single Embedder MUST have a stable
// dimensionality. Stores that index embeddings will reject mismatched
// dims. Implementations should document their dim in their type
// comment (e.g. "Returns 384-dim vectors").
type Embedder interface {
	// Embed produces a vector for the given text. Returns an error
	// for setup-time failures (model not loaded, network unreachable
	// when an API-based embedder needs it). The vector slice is owned
	// by the caller after return.
	Embed(ctx context.Context, text string) ([]float32, error)

	// Dim returns the embedding dimensionality this Embedder
	// produces. Useful for Store backends that allocate fixed-shape
	// matrices and want to fail fast on dimension mismatch.
	Dim() int

	// Name returns a short stable identifier for this embedder
	// (e.g. "ollama:nomic-embed-text", "gomlx:all-MiniLM-L6-v2",
	// "openai:text-embedding-3-small"). Stores tag entries with
	// this name so a recaller can verify it's querying against
	// vectors produced by the same embedder.
	Name() string
}
