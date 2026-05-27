package acl

import (
	"context"
	"errors"
	"testing"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	ac "github.com/voocel/agentcore"
)

func TestToolSpecFromAC(t *testing.T) {
	in := ac.ToolSpec{Name: "search", Description: "d", Parameters: map[string]any{"type": "object"}}
	got := toolSpecFromAC(in)
	if got.Name != "search" || got.Description != "d" || got.Schema["type"] != "object" {
		t.Fatalf("got %+v", got)
	}
}

func TestToolSpecFromAC_NonMapParameters(t *testing.T) {
	got := toolSpecFromAC(ac.ToolSpec{Name: "x", Parameters: "not-a-map"})
	if got.Schema != nil {
		t.Errorf("non-map Parameters should yield nil Schema, got %v", got.Schema)
	}
}

func TestDeltaEventType(t *testing.T) {
	tests := []struct {
		in   event.DeltaKind
		want ac.StreamEventType
	}{
		{event.DeltaText, ac.StreamEventTextDelta},
		{event.DeltaThinking, ac.StreamEventThinkingDelta},
		{event.DeltaToolCall, ac.StreamEventToolCallDelta},
	}
	for _, tt := range tests {
		if got := deltaEventType(tt.in); got != tt.want {
			t.Errorf("deltaEventType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAssistantMessageToAC(t *testing.T) {
	jm := message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "hi"}}}
	got := assistantMessageToAC(jm, model.Usage{Input: 1, Output: 2, TotalTokens: 3}, "stop")
	if got.Role != ac.RoleAssistant || len(got.Content) != 1 || got.Content[0].Text != "hi" {
		t.Fatalf("message = %+v", got)
	}
	if got.StopReason != ac.StopReason("stop") || got.Usage == nil || got.Usage.TotalTokens != 3 {
		t.Errorf("stop/usage = %v / %+v", got.StopReason, got.Usage)
	}
}

// scriptedModel is a model.Model that emits a fixed Chunk script.
type scriptedModel struct{ chunks []model.Chunk }

func (scriptedModel) SupportsTools() bool { return true }
func (m scriptedModel) Stream(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (<-chan model.Chunk, error) {
	ch := make(chan model.Chunk)
	go func() {
		defer close(ch)
		for _, c := range m.chunks {
			select {
			case <-ctx.Done():
				return
			case ch <- c:
			}
		}
	}()
	return ch, nil
}

func TestStreamAdapter_GenerateStreamMapsChunks(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{
		{Delta: "he", DeltaKind: event.DeltaText},
		{Delta: "llo", DeltaKind: event.DeltaText},
		{Done: true, Message: message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "hello"}}}, StopReason: "stop"},
	}}
	var cm ac.ChatModel = ToAC(m)
	stream, err := cm.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var types []ac.StreamEventType
	var final ac.Message
	for ev := range stream {
		types = append(types, ev.Type)
		if ev.Type == ac.StreamEventDone {
			final = ev.Message
		}
	}
	if len(types) != 3 || types[0] != ac.StreamEventTextDelta || types[2] != ac.StreamEventDone {
		t.Fatalf("event types = %v", types)
	}
	if final.Content[0].Text != "hello" || final.StopReason != ac.StopReason("stop") {
		t.Errorf("final = %+v", final)
	}
}

func TestStreamAdapter_GenerateDrainsToFinal(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{
		{Delta: "x", DeltaKind: event.DeltaText},
		{Done: true, Message: message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "done"}}}, StopReason: "stop"},
	}}
	resp, err := ToAC(m).Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content[0].Text != "done" {
		t.Errorf("final message = %+v", resp.Message)
	}
}

func TestStreamAdapter_ErrChunkBecomesError(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{{Err: errors.New("boom")}}}
	_, err := ToAC(m).Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("want error from Err chunk")
	}
}

func TestStreamAdapter_ContextCancelStops(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{{Delta: "a", DeltaKind: event.DeltaText}, {Delta: "b", DeltaKind: event.DeltaText}}}
	ctx, cancel := context.WithCancel(context.Background())
	stream, _ := ToAC(m).GenerateStream(ctx, nil, nil)
	<-stream // first event
	cancel()
	for range stream { // must close without hanging
	}
}
