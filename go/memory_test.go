package muxl

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestLinearMemoryGrowth(t *testing.T) {
	for _, limit := range []uint64{0, 3 * 65536} {
		t.Run(fmt.Sprintf("module_max_%d", limit), func(t *testing.T) {
			var logs bytes.Buffer
			a := &muxlAllocator{logger: slog.New(slog.NewTextHandler(&logs, nil)), initialBytes: 65536, maxBytes: 4 * 65536}
			defer a.release()
			memory := a.Allocate(2*65536, limit)
			defer memory.Free()
			first := memory.Reallocate(65536)
			if len(first) != 65536 || !bytes.Equal(first, make([]byte, len(first))) {
				t.Fatal("new memory must be zero-initialized")
			}
			copy(first, "private signing data")
			first[len(first)-1] = 17
			grown := memory.Reallocate(2*65536 + 1)
			if !bytes.Equal(grown[:len(first)], first) || !bytes.Equal(grown[len(first):], make([]byte, len(grown)-len(first))) {
				t.Fatal("growth must preserve existing bytes and expose zeroed bytes")
			}
			maximum := uint64(4 * 65536)
			if limit != 0 {
				maximum = limit
			}
			if memory.Reallocate(maximum+1) != nil {
				t.Fatal("growth beyond the smaller engine/module ceiling must fail")
			}
			if !strings.Contains(logs.String(), "memory cap exceeded") {
				t.Fatal("expected cap failure must be logged")
			}
			logs.Reset()
			last := memory.Reallocate(maximum)
			if uint64(len(last)) != maximum || !bytes.Equal(last[:len(first)], first) {
				t.Fatal("failed growth must leave memory usable up to its ceiling")
			}
			memory.Free()
			memory.Free()
			a.release()
			a.release()
			if logs.Len() != 0 {
				t.Fatalf("valid growth/release logged errors: %s", &logs)
			}
		})
	}
}

func TestLinearMemoriesAreIndependent(t *testing.T) {
	var logs bytes.Buffer
	a := &muxlAllocator{logger: slog.New(slog.NewTextHandler(&logs, nil)), initialBytes: 65536, maxBytes: 2 * 65536}
	defer a.release()
	first := a.Allocate(65536, 0)
	defer first.Free()
	second := a.Allocate(65536, 0)
	defer second.Free()
	one, two := first.Reallocate(65536), second.Reallocate(65536)
	copy(one, "private stream data")
	if !bytes.Equal(two, make([]byte, len(two))) {
		t.Fatal("one instance exposed another instance's bytes")
	}
	copy(two, "another stream")
	if !bytes.HasPrefix(one, []byte("private stream data")) {
		t.Fatal("instances share writable memory")
	}
	first.Free()
	if len(second.Reallocate(2*65536)) != 2*65536 {
		t.Fatal("closing one instance affected another instance")
	}
	if logs.Len() != 0 {
		t.Fatalf("valid allocations logged errors: %s", &logs)
	}
}

func TestUnaddressableMappingFallsBackToGoMemory(t *testing.T) {
	var logs bytes.Buffer
	a := &muxlAllocator{logger: slog.New(slog.NewTextHandler(&logs, nil)), initialBytes: 65536, maxBytes: ^uint64(0)}
	defer a.release()
	memory := a.Allocate(65536, 0)
	defer memory.Free()
	first := memory.Reallocate(65536)
	copy(first, "fallback")
	if grown := memory.Reallocate(2 * 65536); !bytes.HasPrefix(grown, []byte("fallback")) {
		t.Fatal("an unavailable reservation must preserve the existing heap allocator")
	}
}

func TestConcurrentMemoryCloseAndExecutionCleanup(t *testing.T) {
	var logs bytes.Buffer
	a := &muxlAllocator{logger: slog.New(slog.NewTextHandler(&logs, nil)), initialBytes: 65536, maxBytes: 65536}
	defer a.release()
	memory := a.Allocate(65536, 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		memory.Free()
	}()
	// A concurrent module Close marks the module closed before it calls Free;
	// execution can return and release its allocator in that interval.
	a.release()
	<-done
	if logs.Len() != 0 {
		t.Fatalf("valid concurrent cleanup logged errors: %s", &logs)
	}
}
