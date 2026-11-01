package muxl

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func certificateTestKey(t *testing.T) []byte {
	t.Helper()
	// The secp256k1 generator is a valid public key; these tests exercise
	// callback ownership and failure cleanup, not signature verification.
	key, err := hex.DecodeString("0479be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8")
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSignerInputSurvivesFailedOperation(t *testing.T) {
	var logs bytes.Buffer
	engine, err := NewWASM(t.Context(), WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close(context.Background()) })
	var retained, saved []byte
	_, err = engine.GenCert(t.Context(), CertInput{PubKey: certificateTestKey(t), Sign: func(data []byte) ([]byte, error) {
		retained, saved = data, bytes.Clone(data)
		return nil, errors.New("intentional signing failure")
	}})
	if err == nil || len(retained) == 0 || !bytes.Equal(retained, saved) {
		t.Fatal("failed operation must leave callback-owned input valid")
	}
	if !strings.Contains(logs.String(), "intentional signing failure") {
		t.Fatalf("expected callback failure was not captured: %s", &logs)
	}
}

func TestCloseDuringSignerCallback(t *testing.T) {
	var logs bytes.Buffer
	engine, err := NewWASM(t.Context(), WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close(context.Background()) })
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	go func() {
		_, err := engine.GenCert(t.Context(), CertInput{PubKey: certificateTestKey(t), Sign: func(data []byte) ([]byte, error) {
			close(entered)
			<-release
			return bytes.Repeat([]byte{1}, 64), nil
		}})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("WASM did not enter signer callback")
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closed operation did not join its host callback")
	}
}

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
