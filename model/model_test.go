package model

import (
	"context"
	"errors"
	"testing"

	"github.com/guygrigsby/jess/message"
)

// fakeModel proves the interface is implementable and locks the method set.
type fakeModel struct{ chunks []Chunk }

func (f fakeModel) SupportsTools() bool { return true }
func (f fakeModel) Stream(_ context.Context, _ []message.Message, _ []ToolSpec) (<-chan Chunk, error) {
	ch := make(chan Chunk, len(f.chunks))
	for _, c := range f.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

var _ Model = fakeModel{}

func TestModel_StreamYieldsChunks(t *testing.T) {
	m := fakeModel{chunks: []Chunk{
		{Delta: "hi", DeltaKind: ""},
		{Done: true, Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"},
	}}
	ch, err := m.Stream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 2 || got[0].Delta != "hi" || !got[1].Done || got[1].StopReason != "stop" {
		t.Fatalf("chunks = %+v", got)
	}
}

func TestOnce_EmitsSingleDoneChunk(t *testing.T) {
	m := Once(true, func(_ context.Context, _ []message.Message, _ []ToolSpec) (*Response, error) {
		return &Response{
			Message:    message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "ok"}}},
			Usage:      Usage{Input: 1, Output: 2, TotalTokens: 3},
			StopReason: "stop",
		}, nil
	})
	if !m.SupportsTools() {
		t.Error("SupportsTools should be true")
	}
	ch, _ := m.Stream(context.Background(), nil, nil)
	var got []Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 1 || !got[0].Done || got[0].Message.Text() != "ok" || got[0].Usage.TotalTokens != 3 {
		t.Fatalf("chunks = %+v", got)
	}
}

func TestOnce_EmitsErrChunk(t *testing.T) {
	m := Once(false, func(_ context.Context, _ []message.Message, _ []ToolSpec) (*Response, error) {
		return nil, errors.New("boom")
	})
	ch, _ := m.Stream(context.Background(), nil, nil)
	c := <-ch
	if c.Err == nil || c.Done {
		t.Fatalf("want error chunk, got %+v", c)
	}
}

func TestOnce_NilResponseBecomesErrChunk(t *testing.T) {
	m := Once(false, func(context.Context, []message.Message, []ToolSpec) (*Response, error) {
		return nil, nil // buggy fn: nil response, nil error
	})
	ch, _ := m.Stream(context.Background(), nil, nil)
	c := <-ch
	if c.Err == nil || c.Done {
		t.Fatalf("nil response must yield an error chunk, got %+v", c)
	}
}

func TestOnce_NilFuncBecomesErrChunk(t *testing.T) {
	m := Once(false, nil) // misuse: nil GenerateFunc
	ch, _ := m.Stream(context.Background(), nil, nil)
	c := <-ch
	if c.Err == nil || c.Done {
		t.Fatalf("nil GenerateFunc must yield an error chunk, not panic; got %+v", c)
	}
}
