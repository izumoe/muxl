//go:build unix

package muxl

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func reserveMemory(max uint64) ([]byte, error) {
	if strconv.IntSize != 64 || max == 0 {
		return nil, nil
	}
	if max > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("memory ceiling %d exceeds addressable size", max)
	}
	// Reserve addresses without committing the entire ceiling. Growing the
	// accessible prefix touches only pages the guest actually uses.
	return unix.Mmap(-1, 0, int(max), unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANON)
}

func commitMemory(mapping []byte, old, size uint64) error {
	page := uint64(os.Getpagesize())
	start := (old + page - 1) &^ (page - 1)
	end := min((size+page-1)&^(page-1), uint64(len(mapping)))
	if end <= start {
		return nil
	}
	return unix.Mprotect(mapping[start:end], unix.PROT_READ|unix.PROT_WRITE)
}

func releaseMemory(mapping []byte) error { return unix.Munmap(mapping) }
