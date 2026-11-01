package muxl_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	muxl "github.com/streamplace/muxl/go"
)

func BenchmarkWASMOperations(b *testing.B) {
	for _, initial := range []uint64{16 << 20, 50 << 20} {
		b.Run(fmt.Sprintf("initial_%dMiB", initial>>20), func(b *testing.B) {
			engine := newEngine(b, muxl.WithMemory(initial, 1<<30))
			fixture := readFile(b, fixtureFmp4)
			canonical, err := engine.Canonicalize(context.Background(), fixture)
			if err != nil {
				b.Fatal(err)
			}
			signed := signedM4sStream(b, engine)
			signer := signerInput(b)
			for _, operation := range []struct {
				name string
				run  func() error
			}{
				{"Wrap", func() error {
					return engine.Wrap(context.Background(), bytes.NewReader(canonical), "fmp4", io.Discard)
				}},
				{"Unwrap", func() error {
					_, err := collectEvents(func(events chan<- *muxl.Event) error {
						return engine.UnwrapEvents(context.Background(), bytes.NewReader(canonical), events)
					})
					return err
				}},
				{"Verify", func() error {
					_, err := engine.Verify(context.Background(), bytes.NewReader(signed))
					return err
				}},
				{"Sign", func() error {
					_, err := collectEvents(func(events chan<- *muxl.Event) error {
						return engine.SignSegment(context.Background(), bytes.NewReader(fixture), signer, nil, nil, events)
					})
					return err
				}},
			} {
				b.Run(operation.name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if err := operation.run(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
			b.Run("WrapParallel", func(b *testing.B) {
				b.ReportAllocs()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if err := engine.Wrap(context.Background(), bytes.NewReader(canonical), "fmp4", io.Discard); err != nil {
							b.Error(err)
							return
						}
					}
				})
			})
		})
	}
}
