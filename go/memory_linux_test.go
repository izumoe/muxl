package muxl

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

func TestMappingCommitsOnDemandAndReleasesAfterExecution(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("demand-paged mappings require a 64-bit address space")
	}
	var logs bytes.Buffer
	const ceiling = 8 << 20
	a := &muxlAllocator{logger: slog.New(slog.NewTextHandler(&logs, nil)), initialBytes: 65536, maxBytes: ceiling}
	defer a.release()
	memory := a.Allocate(65536, 0)
	defer memory.Free()
	data := memory.Reallocate(65536)
	data[0], data[len(data)-1] = 1, 2
	address := uint64(uintptr(unsafe.Pointer(unsafe.SliceData(data))))
	mapped, resident := mappingUsage(t, address, address+ceiling)
	if mapped != ceiling || resident == 0 || resident > 65536 {
		t.Fatalf("a %d-byte reservation with two touched pages used %d resident bytes", ceiling, resident)
	}
	memory.Free()
	if mapped, resident := mappingUsage(t, address, address+ceiling); data[0] != 1 || mapped != ceiling || resident == 0 {
		t.Fatal("module close must preserve memory until execution joins")
	}
	a.release()
	if mapped, resident := mappingUsage(t, address, address+ceiling); mapped != 0 || resident != 0 {
		t.Fatal("execution cleanup retained its mapped address space or pages")
	}
	if logs.Len() != 0 {
		t.Fatalf("valid mapping operations logged errors: %s", &logs)
	}
}

func mappingUsage(t *testing.T, start, end uint64) (mapped, resident uint64) {
	t.Helper()
	smaps, err := os.ReadFile("/proc/self/smaps")
	if err != nil {
		t.Fatal(err)
	}
	var selected bool
	for _, line := range strings.Split(string(smaps), "\n") {
		var first, last uint64
		if _, err := fmt.Sscanf(line, "%x-%x", &first, &last); err == nil {
			selected = first < end && start < last
			if selected {
				mapped += min(last, end) - max(first, start)
			}
		}
		if selected && strings.HasPrefix(line, "Rss:") {
			var rss uint64
			if _, err := fmt.Sscanf(line, "Rss: %d kB", &rss); err != nil {
				t.Fatal(err)
			}
			resident += rss * 1024
		}
	}
	return mapped, resident
}
