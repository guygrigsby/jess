// Package memory is jess's durable agent memory: the facts an agent keeps
// across turns and sessions, and the machinery to recall the right ones for
// the current prompt.
//
// The shape:
//
//   - Entry is one memory item — a short text snippet tagged with a Kind, an
//     agent ID, optional Key, and provenance Source. Storage is
//     content-addressable, so callers don't have to invent IDs.
//   - Kind classifies an Entry. KindUser / KindFeedback / KindProject /
//     KindReference are the canonical categories, each with a KindPolicy in the
//     KindRegistry. user/feedback are AlwaysInclude (injected every turn,
//     bypassing recall); project/reference are recall-only. Hosts override per
//     agent via KindRegistry.Set.
//   - Store is the persistence interface: Append + Recall + Forget,
//     concurrency-safe. Three implementations ship: NewInMemoryStore (testing,
//     offline), NewJSONLStore (durable, tombstones, Compact), and
//     NewChromemStore (vector, on chromem-go). Vector-aware backends also
//     satisfy the VectorStore capability interface (SearchVector + Embedder).
//   - Recaller is the read-side query strategy: given the current
//     conversation, return the entries to inject. NewSimpleRecaller (token
//     overlap) and NewVectorRecaller (cosine, needs a VectorStore) compose via
//     NewHybridRecaller (reciprocal rank fusion, K=60).
//   - Embedder turns text into vectors for the vector store and recaller. The
//     embed/gomlx subpackage runs BERT-family sentence-transformers ONNX models
//     in-process via GoMLX's pure-Go backend — no CGO, no subprocess, no ONNX
//     Runtime sidecar.
//   - RememberTool and RecallTool are jess/tool.Tool implementations the model
//     calls to write and read memory itself. Set Entry.Source on tool-written
//     entries so "why do you remember X?" and "forget session Y" stay
//     answerable.
//
// Wiring: hand a Store and Recaller to an agent via jess.WithMemory(store,
// recaller). jess injects the recalled entries as a leading user message before
// each LLM call inside its anti-corruption layer; this package itself stays
// vendor-free. Memory failures never block the LLM call — the inject path
// degrades to no-memory, never no-agent.
//
// Pre-1.0 — API may change before v1. See CHANGELOG.md.
package memory
