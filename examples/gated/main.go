// Command gated shows jess's fail-closed tool gate plus the provenance ledger.
// The model calls a "restart_service" tool (not marked Safe). Without an approver
// the gate denies it; here we wire an approver that reads a y/n from stdin (a
// stand-in for the daemon's Telegram confirm). Because the tool is non-safe, it
// also cannot run without a durable record: we back it with a real SQLite ledger
// so an approved action is committed first, then executes. After the run we read
// the chain back to show why the agent acted.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/gate"
	"github.com/guygrigsby/jess/ledger"
)

// capturingLedger wraps the durable SQLite ledger to remember the run id so the
// example can read the chain back afterward. A real host would query the ledger
// by a run id it already tracks.
type capturingLedger struct {
	*ledger.SQLite
	runID string
}

func (c *capturingLedger) Record(e ledger.Event) error {
	if e.RunID != "" {
		c.runID = e.RunID
	}
	return c.SQLite.Record(e)
}

func (c *capturingLedger) CommitAction(e ledger.Event) error {
	if e.RunID != "" {
		c.runID = e.RunID
	}
	return c.SQLite.CommitAction(e)
}

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

	// A durable ledger. The non-safe restart_service tool cannot run without a
	// committed action record, so this is what makes "approve -> it actually
	// runs" work. With a DiscardSink the enforcement would deny it even after
	// approval (no durable record, no action).
	dir, err := os.MkdirTemp("", "gated-ledger")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	db, err := ledger.OpenSQLite(filepath.Join(dir, "ledger.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	led := &capturingLedger{SQLite: db}

	stdin := bufio.NewReader(os.Stdin)
	agent := jess.New(
		jess.WithModel(model),
		jess.WithTools(restartTool{}),
		jess.WithApprover(stdinApprover(stdin)),
		jess.WithLedger(led),
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

	// Read the chain back: why did the agent act?
	chain, err := led.Chain(led.runID)
	if err != nil {
		log.Fatalf("read chain: %v", err)
	}
	fmt.Printf("\n[ledger] run %s\n", led.runID)
	fmt.Printf("  request: %s\n", chain.Request.Args)
	for _, a := range chain.Actions {
		outcome := "not executed"
		if a.Intent.Verdict == ledger.VerdictAllowed {
			switch {
			case a.Result.Err != "":
				outcome = "error: " + a.Result.Err
			case a.Result.EventID == (ledger.EventID{}):
				outcome = "allowed, no result recorded"
			default:
				outcome = "ran"
			}
		}
		fmt.Printf("  action: %s %s (verdict=%s, %s)\n", a.Intent.Tool, a.Intent.Args, a.Intent.Verdict, outcome)
	}
}
