// Package memory adds durable agent memory on top of agentcore.
//
// The shape:
//
//   - Entry is one memory item — a short text snippet tagged with a
//     kind ("preference", "fact", "decision", etc.), an agent ID, and
//     timestamp. Storage is content-addressable enough that callers
//     don't have to invent IDs.
//   - Store is the persistence interface: Append + Recall + Forget.
//     Implementations ship for in-memory (testing) and JSONL on disk
//     (default for hosts). Callers can write their own — sqlite,
//     postgres with pgvector, redis, etc.
//   - Recaller is the read-side query strategy: given the current
//     conversation, return the entries to inject. Default
//     implementations: tag match, recency, and a simple TF-IDF; hosts
//     can plug semantic search by satisfying the interface.
//
// Integration with agentcore happens through two adapters:
//
//   - ContextManager: jess/memory.NewContextManager wraps a Store +
//     Recaller and injects matched entries as a leading user message
//     (configurable) before every LLM call. Pass to
//     agentcore.WithContextManager.
//   - OnMessage: jess/memory.NewMessagePersister wraps a Store and
//     appends assistant turns and explicit user "remember this"
//     directives. Pass to agentcore.WithOnMessage.
//
// Status: skeleton — interfaces shipped, implementations land in
// subsequent commits. Don't import outside of jess just yet.
package memory
