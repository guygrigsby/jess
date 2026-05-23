package jess

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestModelID_ProviderAndModel(t *testing.T) {
	cases := []struct {
		in       ModelID
		provider string
		model    string
	}{
		{"openai/gpt-5.4-mini", "openai", "gpt-5.4-mini"},
		{"anthropic/claude-sonnet-4-6", "anthropic", "claude-sonnet-4-6"},
		// No slash: provider is empty, model is the whole thing. Lets
		// providers that don't care about routing receive the full ID
		// without special-casing missing slashes.
		{"naked-id", "", "naked-id"},
		// Empty input is well-defined (both segments are empty).
		{"", "", ""},
		// Multiple slashes: only the first is the separator. Some
		// model IDs include version paths.
		{"openai/responses/v1", "openai", "responses/v1"},
	}
	for _, c := range cases {
		t.Run(string(c.in), func(t *testing.T) {
			if got := c.in.Provider(); got != c.provider {
				t.Errorf("Provider() = %q, want %q", got, c.provider)
			}
			if got := c.in.Model(); got != c.model {
				t.Errorf("Model() = %q, want %q", got, c.model)
			}
		})
	}
}

func TestDeltaKind_String(t *testing.T) {
	cases := map[DeltaKind]string{
		DeltaText:      "text",
		DeltaReasoning: "reasoning",
		DeltaUsage:     "usage",
		DeltaToolCall:  "tool_call",
		DeltaError:     "error",
		DeltaKind(99):  "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("DeltaKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

// fakeProvider is a tiny in-memory Provider used to exercise the
// ProviderRegistry and to prove the interface is satisfiable without
// requiring a real LLM SDK.
type fakeProvider struct {
	name    string
	deltas  []Delta // emitted in order, then channel closed
	setupErr error  // returned synchronously when non-nil
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Stream(ctx context.Context, _ Request) (<-chan Delta, error) {
	if p.setupErr != nil {
		return nil, p.setupErr
	}
	ch := make(chan Delta, len(p.deltas))
	go func() {
		defer close(ch)
		for _, d := range p.deltas {
			select {
			case <-ctx.Done():
				return
			case ch <- d:
			}
		}
	}()
	return ch, nil
}

func TestProvider_InterfaceContract_Streaming(t *testing.T) {
	usage := &Usage{InputTokens: 10, OutputTokens: 5}
	p := &fakeProvider{
		name: "fake",
		deltas: []Delta{
			{Kind: DeltaText, Text: "hello "},
			{Kind: DeltaText, Text: "world"},
			{Kind: DeltaUsage, Usage: usage},
		},
	}
	ch, err := p.Stream(context.Background(), Request{Model: "fake/test"})
	if err != nil {
		t.Fatalf("Stream errored: %v", err)
	}
	var got []Delta
	for d := range ch {
		got = append(got, d)
	}
	if len(got) != 3 {
		t.Fatalf("got %d deltas, want 3: %+v", len(got), got)
	}
	if got[0].Text+got[1].Text != "hello world" {
		t.Errorf("text reconstruction: %q + %q", got[0].Text, got[1].Text)
	}
	if got[2].Kind != DeltaUsage || got[2].Usage != usage {
		t.Errorf("final delta should carry usage: %+v", got[2])
	}
}

func TestProvider_SetupErrorReturnedSynchronously(t *testing.T) {
	sentinel := errors.New("auth missing")
	p := &fakeProvider{name: "fake", setupErr: sentinel}
	ch, err := p.Stream(context.Background(), Request{Model: "fake/x"})
	if ch != nil {
		t.Error("channel should be nil on setup error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("setup error not propagated: %v", err)
	}
}

func TestProvider_ContextCancelStopsStream(t *testing.T) {
	// Buffer one delta, cancel ctx before consuming. The provider's
	// goroutine should return without panicking; channel must close.
	p := &fakeProvider{
		name:   "fake",
		deltas: []Delta{{Kind: DeltaText, Text: "x"}, {Kind: DeltaText, Text: "y"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := p.Stream(ctx, Request{Model: "fake/x"})
	if err != nil {
		t.Fatalf("Stream errored: %v", err)
	}
	// Drain to confirm close. Test deadlocks if the provider violates
	// the contract by not closing the channel.
	for range ch {
	}
}

func TestNewProviderRegistry_EmptyValid(t *testing.T) {
	r, err := NewProviderRegistry()
	if err != nil {
		t.Fatalf("empty registry errored: %v", err)
	}
	if names := r.Names(); len(names) != 0 {
		t.Errorf("expected empty Names, got %v", names)
	}
}

func TestNewProviderRegistry_RejectsDuplicate(t *testing.T) {
	a := &fakeProvider{name: "openai"}
	b := &fakeProvider{name: "openai"}
	_, err := NewProviderRegistry(a, b)
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	// Typed error lets callers inspect Name without parsing the string.
	var dupErr *duplicateProviderErr
	if !errors.As(err, &dupErr) {
		t.Fatalf("expected *duplicateProviderErr, got %T: %v", err, err)
	}
	if dupErr.Name != "openai" {
		t.Errorf("duplicateProviderErr.Name = %q, want openai", dupErr.Name)
	}
}

func TestNewProviderRegistry_RejectsNilAndEmptyName(t *testing.T) {
	if _, err := NewProviderRegistry(nil); !errors.Is(err, errNilProvider) {
		t.Errorf("nil provider should return errNilProvider, got %v", err)
	}
	if _, err := NewProviderRegistry(&fakeProvider{name: ""}); !errors.Is(err, errEmptyProviderName) {
		t.Errorf("empty name should return errEmptyProviderName, got %v", err)
	}
}

func TestProviderRegistry_Get(t *testing.T) {
	openai := &fakeProvider{name: "openai"}
	anthropic := &fakeProvider{name: "anthropic"}
	r, err := NewProviderRegistry(openai, anthropic)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := r.Get("openai")
	if !ok || got != openai {
		t.Errorf("Get openai: ok=%v got=%v", ok, got)
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("unknown provider should return ok=false")
	}

	// Names should round-trip every registration.
	names := r.Names()
	sort.Strings(names)
	want := []string{"anthropic", "openai"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Names = %v, want %v", names, want)
	}
}

// Compile-time guarantee that fakeProvider satisfies Provider — drops
// out as a build error rather than a runtime nil-panic if the interface
// drifts and we forget to update the fake.
var _ Provider = (*fakeProvider)(nil)

// Smoke-test that DeltaKind.String() output is stable enough to use in
// logs without surprises.
func TestDeltaKind_StringAllValuesNonEmpty(t *testing.T) {
	for k := DeltaText; k <= DeltaError; k++ {
		if strings.TrimSpace(k.String()) == "" {
			t.Errorf("DeltaKind(%d).String() is blank — every named kind needs a label", k)
		}
	}
}
