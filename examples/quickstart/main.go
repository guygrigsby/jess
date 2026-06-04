// Command quickstart shows jess end to end with no network access: an in-memory
// store seeds a core memory, a local echo model reveals what the agent received
// (including the injected memory), and the run is driven through jess.New ->
// jess.Stream with its live event channel.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/memory"
)

func main() {
	ctx := context.Background()

	// 1. Durable memory. InMemoryStore keeps the quickstart offline and fast;
	//    swap in JSONLStore or ChromemStore (+ memory/embed/gomlx) for
	//    persistence or vector recall.
	store := memory.NewInMemoryStore()
	if _, err := store.Append(ctx, memory.Entry{
		AgentID: "demo",
		Kind:    string(memory.KindUser), // user Kind is AlwaysInclude: injected every turn
		Text:    "User prefers concise, example-first answers.",
	}); err != nil {
		log.Fatalf("seed memory: %v", err)
	}

	// 2. A local model. jess.Once adapts a one-shot function into an
	//    agentcore.ChatModel; here it echoes what the agent received, so the
	//    injected memory is visible in the reply.
	echo := jess.Once(false, func(_ context.Context, msgs []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		var b strings.Builder
		for _, m := range msgs {
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.TextContent())
		}
		return &ac.LLMResponse{Message: ac.Message{
			Role:    ac.RoleAssistant,
			Content: []ac.ContentBlock{ac.TextBlock(b.String())},
		}}, nil
	})

	// 3. Wire it all together. New returns a real *agentcore.Agent.
	agent := jess.New(
		jess.WithModel(echo),
		jess.WithAgentID("demo"),
		jess.WithMemory(store, memory.NewSimpleRecaller()),
		jess.WithLedger(ledger.DiscardSink{}), // quiet for the demo
	)

	// 4. Drive a run and observe its event channel.
	ch, wait := jess.Stream(ctx, agent, "What kind of answers do I like?")
	for ev := range ch {
		switch ev.Type {
		case ac.EventToolExecStart:
			fmt.Printf("-> tool %s\n", ev.Tool)
		case ac.EventError:
			fmt.Printf("! error: %v\n", ev.Err)
		}
	}

	// 5. The injected core memory reached the model; the run completed.
	if sum := wait(); sum == nil {
		log.Fatal("no summary")
	}
	fmt.Println("ok: memory injected, run completed")
}
