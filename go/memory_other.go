//go:build !unix

package muxl

// Platforms without anonymous mappings keep the existing Go heap allocator.
func reserveMemory(uint64) ([]byte, error)      { return nil, nil }
func commitMemory([]byte, uint64, uint64) error { return nil }
func releaseMemory([]byte) error                { return nil }
