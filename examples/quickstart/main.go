// Command quickstart shows the jess facade end to end with no network access:
// an in-memory store seeds a core memory, a local echo model reveals what the
// agent received (including the injected memory), and the run is driven through
// jess.New -> Agent.Prompt with its live event stream.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
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

	// 2. A local model. model.Once adapts a one-shot function into the
	//    streaming model.Model; here it echoes what the agent received, so the
	//    injected memory is visible in the reply.
	echo := model.Once(false, func(_ context.Context, msgs []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		var b strings.Builder
		for _, m := range msgs {
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Text())
		}
		return &model.Response{
			Message: message.Message{
				Role:    message.RoleAssistant,
				Content: []message.ContentBlock{{Kind: message.BlockText, Text: b.String()}},
			},
			StopReason: "stop",
		}, nil
	})

	// 3. Wire it all behind the facade.
	agent, err := jess.New(
		jess.WithModel(echo),
		jess.WithAgentID("demo"),
		jess.WithMemory(store, memory.NewSimpleRecaller()),
	)
	if err != nil {
		log.Fatalf("jess.New: %v", err)
	}

	// 4. Drive a run and observe its event stream.
	run, err := agent.Prompt(ctx, "What kind of answers do I like?")
	if err != nil {
		log.Fatalf("Prompt: %v", err)
	}
	for ev := range run.Events() {
		switch ev.Kind {
		case event.KindToolStart:
			fmt.Printf("-> tool %s\n", ev.Tool)
		case event.KindError:
			fmt.Printf("! error: %v\n", ev.Err)
		}
	}

	// 5. Final result. The echoed assistant text contains the injected core
	//    memory, proving memory reached the model through the facade.
	res, err := run.Wait()
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	for _, m := range res.Messages {
		if m.Role == message.RoleAssistant {
			fmt.Println("\nassistant saw:\n" + m.Text())
		}
	}
}
