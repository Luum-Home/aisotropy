package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/fugue-labs/gollem/core"
)

// scriptedClient is a minimal Client whose StreamChat replays a fixed sequence
// of Tokens on a fresh channel — enough to drive the gollemStream adapter
// deterministically without any network or the word-by-word StubClient timing.
type scriptedClient struct {
	name   string
	tokens []Token
	err    error // when set, StreamChat itself fails
}

func (c *scriptedClient) Name() string { return c.name }

func (c *scriptedClient) StreamChat(_ context.Context, _, _ string) (<-chan Token, error) {
	if c.err != nil {
		return nil, c.err
	}
	ch := make(chan Token, len(c.tokens))
	for _, t := range c.tokens {
		ch <- t
	}
	close(ch)
	return ch, nil
}

func streamMessages() []core.ModelMessage {
	return []core.ModelMessage{
		core.ModelRequest{Parts: []core.ModelRequestPart{core.UserPromptPart{Content: "hi"}}},
	}
}

// TestGollemStream_HappyPath drives RequestStream end to end: PartStart, one
// delta per text Token, then PartEnd; Response() reflects the accumulated text
// and Usage() reflects the terminal Token's real counts.
func TestGollemStream_HappyPath(t *testing.T) {
	client := &scriptedClient{
		name: "scripted",
		tokens: []Token{
			{Text: "Hel"},
			{Text: "lo"},
			{Done: true, Usage: &Usage{InputTokens: 5, OutputTokens: 9}},
		},
	}
	m := NewGollemModel(client)

	stream, err := m.RequestStream(context.Background(), streamMessages(), nil, &core.ModelRequestParameters{})
	if err != nil {
		t.Fatalf("RequestStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// First event is always a PartStartEvent.
	ev, err := stream.Next()
	if err != nil {
		t.Fatalf("Next(start): %v", err)
	}
	if _, ok := ev.(core.PartStartEvent); !ok {
		t.Fatalf("first event = %T, want PartStartEvent", ev)
	}

	var deltas int
	var sawEnd bool
	for {
		ev, err := stream.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if _, ok := ev.(core.PartEndEvent); ok {
			sawEnd = true
			break
		}
		if _, ok := ev.(core.PartDeltaEvent); ok {
			deltas++
		}
	}
	if !sawEnd {
		t.Fatal("never saw PartEndEvent")
	}
	if deltas != 2 {
		t.Errorf("delta events = %d, want 2", deltas)
	}

	// After the end, further Next() returns io.EOF-style default state.
	if _, err := stream.Next(); err == nil {
		t.Error("expected an error/EOF after the stream ended")
	}

	if resp := stream.Response(); resp == nil {
		t.Fatal("Response() = nil")
	}
	u := stream.Usage()
	if u.InputTokens != 5 || u.OutputTokens != 9 {
		t.Errorf("Usage() = %+v, want {5 9}", u)
	}
}

// TestGollemStream_MidStreamError asserts a Token.Err surfaces from Next() as a
// real error and moves the stream to Done.
func TestGollemStream_MidStreamError(t *testing.T) {
	boom := errors.New("boom")
	client := &scriptedClient{
		name:   "scripted",
		tokens: []Token{{Text: "part"}, {Err: boom}},
	}
	m := NewGollemModel(client)

	stream, err := m.RequestStream(context.Background(), streamMessages(), nil, &core.ModelRequestParameters{})
	if err != nil {
		t.Fatalf("RequestStream: %v", err)
	}

	// Drain until the error surfaces.
	var gotErr error
	for i := 0; i < 5; i++ {
		_, e := stream.Next()
		if e != nil {
			gotErr = e
			break
		}
	}
	if !errors.Is(gotErr, boom) {
		t.Fatalf("Next() error = %v, want boom", gotErr)
	}
}

// TestGollemStream_NoUsage covers the Usage()==nil branch: a stream that ends
// with no usage-bearing terminal Token reports the zero Usage.
func TestGollemStream_NoUsage(t *testing.T) {
	client := &scriptedClient{name: "scripted", tokens: []Token{{Text: "x"}, {Done: true}}}
	m := NewGollemModel(client)

	stream, err := m.RequestStream(context.Background(), streamMessages(), nil, &core.ModelRequestParameters{})
	if err != nil {
		t.Fatalf("RequestStream: %v", err)
	}
	for {
		ev, err := stream.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if _, ok := ev.(core.PartEndEvent); ok {
			break
		}
	}
	if u := stream.Usage(); u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("Usage() = %+v, want zero", u)
	}
}

// TestGollemStream_StreamChatError asserts RequestStream propagates a
// StreamChat setup failure (wrapped with the model name).
func TestGollemStream_StreamChatError(t *testing.T) {
	client := &scriptedClient{name: "scripted", err: errors.New("dial failed")}
	m := NewGollemModel(client)

	_, err := m.RequestStream(context.Background(), streamMessages(), nil, &core.ModelRequestParameters{})
	if err == nil {
		t.Fatal("expected RequestStream to propagate the StreamChat error")
	}
}
