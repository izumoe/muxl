//go:build unix

package muxl_test

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"strconv"
	"testing"

	muxl "github.com/streamplace/muxl/go"
)

func TestWASMLinearMemoryAllocationBudget(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("demand-paged backing requires a 64-bit address space")
	}
	engine := newEngine(t, muxl.WithMemory(16<<20, 1<<30))
	canonical, err := engine.Canonicalize(context.Background(), readFile(t, fixtureFmp4))
	if err != nil {
		t.Fatal(err)
	}
	wrap := func() error {
		return engine.Wrap(context.Background(), bytes.NewReader(canonical), "fmp4", io.Discard)
	}
	if err := wrap(); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := wrap(); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	// The fixture is small. A fresh minimum-sized WASM backing buffer must
	// not dominate the Go heap on every operation after engine compilation.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 8<<20 {
		t.Fatalf("one wrap allocated %d Go heap bytes; want less than 8 MiB", allocated)
	}
}
