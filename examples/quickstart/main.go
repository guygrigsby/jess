// quickstart shows the minimal end-to-end wiring of jess memory:
// build an in-process embedder, attach a vector store, seed it with
// a few memories, build a layered context-manager projection, and
// print what the LLM would see as a system prompt prepended to a
// fresh user message.
//
// What this DOESN'T do: actually call an LLM. The point is to
// demonstrate jess's memory pipeline standalone — the projected
// messages are what you'd hand to agentcore.WithContextManager
// in a real host.
//
// Run:
//
//	go run ./examples/quickstart
//
// Expect a ~90MB ONNX model download on first run (cached
// at ~/.cache/huggingface afterward; subsequent runs are warm).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/memory/embed/gomlx"
)

func main() {
	ctx := context.Background()

	// 1. Pure-Go embedder. Downloads sentence-transformers/all-MiniLM-L6-v2
	//    on first call; cached after. Zero CGO, zero subprocess.
	fmt.Println("loading embedder (downloads ~90MB on first run)...")
	emb, err := gomlx.NewEmbedder(gomlx.Options{})
	if err != nil {
		log.Fatalf("embedder: %v", err)
	}

	// 2. Vector store backed by chromem-go, persisted to gob on disk.
	dir, _ := os.UserCacheDir()
	storePath := filepath.Join(dir, "jess-quickstart")
	store, err := memory.NewChromemStore(emb, memory.ChromemOptions{
		Path: storePath,
	})
	if err != nil {
		log.Fatalf("store: %v", err)
	}

	// 3. Seed with a few memories of different Kinds. KindUser and
	//    KindFeedback always surface; KindProject only on relevance.
	seed := []memory.Entry{
		{Kind: string(memory.KindUser), AgentID: "main", Text: "user is a senior Go engineer"},
		{Kind: string(memory.KindFeedback), AgentID: "main", Text: "prefer terse responses with concrete examples"},
		{Kind: string(memory.KindProject), AgentID: "main", Text: "current sprint: ship the new vector memory feature by Friday"},
		{Kind: string(memory.KindProject), AgentID: "main", Text: "decision: use chromem-go pinned to main for now"},
		{Kind: string(memory.KindReference), AgentID: "main", Text: "vector DB landscape comparison at shaharia.com/blog/choosing-embeddable-vector-database-go-application"},
	}
	for _, e := range seed {
		if _, err := store.Append(ctx, e); err != nil {
			log.Fatalf("append: %v", err)
		}
	}

	// 4. Hybrid retrieval: vector + token-overlap, fused via RRF.
	recaller := memory.NewHybridRecaller(
		memory.NewVectorRecaller(),
		memory.NewSimpleRecaller(),
	)

	// 5. ContextManager projects core + relevant memories into the
	//    prompt view on each LLM call.
	cm := memory.NewContextManager(store, recaller, memory.ContextManagerOptions{
		AgentID: "main",
	})

	// 6. Simulate a user turn. Project the prompt view the model
	//    would see and print the memory message verbatim.
	userMsg := agentcore.Message{
		Role: agentcore.Role("user"),
		Content: []agentcore.ContentBlock{
			agentcore.TextBlock("What's the status of the vector memory work?"),
		},
	}
	proj, err := cm.Project(ctx, []agentcore.AgentMessage{userMsg})
	if err != nil {
		log.Fatalf("project: %v", err)
	}

	fmt.Println("=== projected prompt view ===")
	for i, msg := range proj.Messages {
		fmt.Printf("\n[message %d, role=%s]\n%s\n", i, msg.GetRole(), msg.TextContent())
	}

	// 7. Demo: save a new memory via the RememberTool (what the model
	//    would do at runtime). Then project again to show it surfaced.
	tool := memory.NewRememberTool(store, memory.RememberOptions{AgentID: "main"})
	if _, err := tool.Execute(
		memory.WithSource(ctx, memory.Source{
			SessionID: "quickstart",
			MessageID: "demo-msg-1",
		}),
		[]byte(`{"kind":"project","text":"benchmark: gomlx embedder ~50ms per call on M-series CPU","reason":"performance result worth keeping"}`),
	); err != nil {
		log.Fatalf("remember: %v", err)
	}

	proj2, _ := cm.Project(ctx, []agentcore.AgentMessage{
		agentcore.Message{
			Role: agentcore.Role("user"),
			Content: []agentcore.ContentBlock{
				agentcore.TextBlock("What benchmarks do we have?"),
			},
		},
	})
	fmt.Println("\n=== after saving a new memory + new query ===")
	fmt.Println(proj2.Messages[0].TextContent())

	fmt.Printf("\nstore persisted to %s\n", storePath)
}
