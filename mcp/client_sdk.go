package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// sdkClient is the real MCP client: a stdio server subprocess plus its session.
// It is the ONLY type that touches the MCP SDK, and the only implementation of
// [client] outside tests. All SDK types stay inside this file.
type sdkClient struct {
	session *mcpsdk.ClientSession
}

// dialStdio starts srv's command as a stdio MCP server subprocess, runs the
// initialize handshake and returns a connected client. The subprocess is shut
// down by Close (which closes stdin and waits, escalating to SIGTERM/SIGKILL).
func dialStdio(ctx context.Context, srv Server) (client, error) {
	if strings.TrimSpace(srv.Command) == "" {
		return nil, fmt.Errorf("server %q: empty command", srv.Name)
	}
	cmd := exec.Command(srv.Command, srv.Args...)
	cmd.Env = append(os.Environ(), srv.Env...)

	c := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "jess", Version: "v1"}, nil)
	session, err := c.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &sdkClient{session: session}, nil
}

func (s *sdkClient) listTools(ctx context.Context) ([]toolDef, error) {
	res, err := s.session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	defs := make([]toolDef, 0, len(res.Tools))
	for _, t := range res.Tools {
		defs = append(defs, toolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: asSchemaMap(t.InputSchema),
			ReadOnly:    t.Annotations != nil && t.Annotations.ReadOnlyHint,
		})
	}
	return defs, nil
}

// callTool calls the named tool and reports its result under the [client]
// contract: an MCP IsError result is the tool's own reported failure, meant
// for the model to read and self-correct, so it comes back as text (prefixed
// "error: ") with a nil error rather than a Go error. A non-nil error here is
// a transport or protocol failure; a closed connection wraps ErrServerGone so
// callers can check it with errors.Is.
func (s *sdkClient) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	res, err := s.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		if errors.Is(err, mcpsdk.ErrConnectionClosed) {
			return "", fmt.Errorf("tools/call %q: %w: %w", name, ErrServerGone, err)
		}
		return "", fmt.Errorf("tools/call %q: %w", name, err)
	}
	text := collectText(res.Content)
	if res.IsError {
		return "error: " + text, nil
	}
	return text, nil
}

func (s *sdkClient) Close() error { return s.session.Close() }

// asSchemaMap coerces the SDK's InputSchema (any; from the client it is the
// server's JSON, a map[string]any) into a map. Anything else yields nil, which
// the adapter replaces with a minimal object schema.
func asSchemaMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// collectText joins the text of every text-content block in the tool result.
func collectText(content []mcpsdk.Content) string {
	var parts []string
	for _, c := range content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}
