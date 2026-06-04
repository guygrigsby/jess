// Command gated shows jess's fail-closed tool gate with an interactive approver.
// The model calls a "restart_service" tool (not marked Safe). Without an approver
// the gate denies it; here we wire an approver that prints the request and reads
// a y/n answer from stdin — a stand-in for the daemon's Telegram confirm.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/gate"
)

// restartTool restarts a named service. It is deliberately NOT marked Safe so
// the gate must ask the operator before running it.
type restartTool struct{}

func (restartTool) Name() string          { return "restart_service" }
func (restartTool) Description() string   { return "restart a named service" }
func (restartTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (restartTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	fmt.Printf("[tool] restarting service with args %s\n", args)
	return json.RawMessage(`"restarted"`), nil
}

// stdinApprover is a synchronous human-in-the-loop approver. The daemon would
// replace this with an async Telegram flow.
func stdinApprover(r *bufio.Reader) gate.Approver {
	return func(_ context.Context, req gate.Request) (bool, string) {
		fmt.Printf("\n[gate] tool=%q\n", req.Tool)
		if req.Preview != "" {
			fmt.Printf("       preview: %s\n", req.Preview)
		}
		if len(req.Args) > 0 {
			fmt.Printf("       args: %s\n", req.Args)
		}
		fmt.Print("Allow? [y/N] ")
		line, _ := r.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) == "y" {
			return true, "operator approved"
		}
		return false, "operator denied"
	}
}

func main() {
	var calls int
	// Echo model that calls restart_service once, then stops.
	model := jess.Once(true, func(_ context.Context, _ []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		calls++
		if calls == 1 {
			return &ac.LLMResponse{
				Message: ac.Message{
					Role: ac.RoleAssistant,
					Content: []ac.ContentBlock{
						ac.ToolCallBlock(ac.ToolCall{
							ID:   "call_1",
							Name: "restart_service",
							Args: json.RawMessage(`{"name":"nginx"}`),
						}),
					},
					StopReason: ac.StopReasonToolUse,
				},
			}, nil
		}
		return &ac.LLMResponse{
			Message: ac.Message{
				Role:       ac.RoleAssistant,
				Content:    []ac.ContentBlock{ac.TextBlock("done")},
				StopReason: ac.StopReasonStop,
			},
		}, nil
	})

	stdin := bufio.NewReader(os.Stdin)
	agent := jess.New(
		jess.WithModel(model),
		jess.WithTools(restartTool{}),
		jess.WithApprover(stdinApprover(stdin)),
		jess.WithAudit(audit.DiscardSink{}),
	)

	fmt.Println("Sending: restart nginx")
	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for ev := range ch {
		if ev.Type == ac.EventError {
			fmt.Printf("error: %v\n", ev.Err)
		}
	}
	if sum := wait(); sum == nil {
		log.Fatal("no summary")
	}
	fmt.Println("run complete")
}
