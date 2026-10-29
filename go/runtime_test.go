package muxl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStreamingCancellationClosesOwnedPipes(t *testing.T) {
	logs, err := os.CreateTemp(t.TempDir(), "muxl.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logs.Close() })
	engine, err := NewWASM(context.Background(), WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close(context.Background()) })
	fixture, err := os.ReadFile("../samples/fixtures/h264-opus-frag.mp4")
	if err != nil {
		t.Fatal(err)
	}
	for _, openInput := range []bool{false, true} {
		t.Run(fmt.Sprintf("open_input=%t", openInput), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var input io.Reader = bytes.NewReader(fixture)
			events := make(chan *Event)
			var written chan error
			if openInput {
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				input = reader
				events = make(chan *Event, 16)
				written = make(chan error, 1)
				go func() {
					_, err := writer.Write(fixture)
					written <- err
				}()
			}
			done := make(chan error, 1)
			go func() { done <- engine.SegmentEvents(ctx, input, events) }()
			select {
			case event := <-events:
				if event.Type != "init" {
					t.Fatalf("expected native init event, got %q", event.Type)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("native segmenter did not initialize")
			}
			if written != nil {
				select {
				case err := <-written:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("native segmenter did not consume the fixture")
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected caller cancellation, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation left native pipe I/O blocked")
			}
			if engine.runtime.Module(fmt.Sprintf("muxl-%d", engine.counter.Load())) != nil {
				t.Fatal("canceled module must be closed before the call returns")
			}
		})
	}
	output, err := os.ReadFile(logs.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("expected caller cancellation must not log errors: %s", output)
	}
}

func TestParserFailureClosesModule(t *testing.T) {
	engine, err := NewWASM(context.Background(), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close(context.Background()) })
	fixture, err := os.ReadFile("../samples/fixtures/h264-opus-frag.mp4")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := engine.Canonicalize(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	// A real wrap command writes MP4, which the event reader cannot
	// decode. Its parse error must stop and join the writer, not strand it.
	err = engine.runWith(context.Background(), []string{"muxl", "wrap", "-", "-", "--format", "fmp4"}, nil, false,
		bytes.NewReader(canonical), nil, nil, nil, nil, nil, make(chan *Event))
	if err == nil || !strings.Contains(err.Error(), "parsing events") {
		t.Fatalf("expected native output parsing error, got %v", err)
	}
	if engine.runtime.Module(fmt.Sprintf("muxl-%d", engine.counter.Load())) != nil {
		t.Fatal("module must be closed before returning the parser failure")
	}
}
