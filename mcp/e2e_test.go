//go:build e2e

// This test spins up a real MCP server over stdio and exercises the actual SDK
// client path (dial, initialize, tools/list, tools/call), which the unit
// tests fake out. It is off by default; run with:
//
//	go test -tags e2e ./mcp/ -v
//
// It needs `npx` (Node) on PATH and network access (npx fetches the reference
// "everything" server on first run); it skips when npx is absent.
package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestE2E_RealMCPServer(t *testing.T) {
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not found; skipping live MCP e2e")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	srv := Server{
		Name:    "everything",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-everything"},
	}
	tools, closer, err := Tools(ctx, []Server{srv}, t.Logf)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	defer func() { _ = closer.Close() }()

	if len(tools) == 0 {
		t.Fatal("real MCP server returned no tools (dial/list failed?)")
	}
	for _, tl := range tools {
		t.Logf("tool: %s, %s", tl.Name(), tl.Description())
	}

	// The reference server exposes an "echo" tool; call it and check the round trip.
	var echo interface {
		Name() string
		Execute(context.Context, json.RawMessage) (json.RawMessage, error)
	}
	for _, tl := range tools {
		if strings.Contains(tl.Name(), "echo") {
			echo = tl
			break
		}
	}
	if echo == nil {
		t.Skip("no echo tool on this server version; list path verified")
	}
	out, err := echo.Execute(ctx, json.RawMessage(`{"message":"ping-mcp"}`))
	if err != nil {
		t.Fatalf("call %s: %v", echo.Name(), err)
	}
	if !strings.Contains(string(out), "ping-mcp") {
		t.Errorf("echo did not round-trip; got %s", out)
	}
}
