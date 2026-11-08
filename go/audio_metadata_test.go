package muxl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/hyphacoop/go-dasl/drisl"
	muxl "github.com/streamplace/muxl/go"
)

func TestEventPreservesAudioGroupAndLabel(t *testing.T) {
	wire, err := drisl.Marshal(map[string]any{
		"type": "init",
		"catalog": map[string]any{"audio": map[string]any{"renditions": map[string]any{
			"audio2": map[string]any{
				"codec": "opus", "container": map[string]any{"kind": "cmaf", "timescale": 48000, "trackId": 2},
				"sampleRate": 48000, "numberOfChannels": 2, "alternateGroup": 3, "label": "Music",
			},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var event muxl.Event
	if err := drisl.NewDecoder(bytes.NewReader(wire)).Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Catalog == nil || event.Catalog.Audio == nil {
		t.Fatal("audio catalog missing")
	}
	track := event.Catalog.Audio.Renditions["audio2"]
	if track.AlternateGroup != 3 || track.Label != "Music" {
		t.Fatalf("audio metadata = %d, %q", track.AlternateGroup, track.Label)
	}
}

func TestSignSegmentPreservesAudioMetadata(t *testing.T) {
	eng := newEngine(t)
	input := readFile(t, fixtureFmp4)
	for _, dynamic := range []bool{false, true} {
		name := "static manifest"
		if dynamic {
			name = "dynamic manifest"
		}
		t.Run(name, func(t *testing.T) {
			in := signerInput(t)
			in.TrackRemap = map[uint32]uint32{2: 9}
			in.TrackMeta = map[uint32]muxl.TrackMeta{9: {AlternateGroup: 3, Label: "Music: 音楽"}}
			if dynamic {
				in.TrackManifestFn = func() ([]byte, error) { return in.TrackManifest, nil }
				in.WrapperManifestFn = func() ([]byte, error) { return in.WrapperManifest, nil }
			}
			events, err := collectEvents(func(events chan<- *muxl.Event) error {
				return eng.SignSegment(context.Background(), bytes.NewReader(input), in, nil, nil, events)
			})
			if err != nil {
				t.Fatal(err)
			}
			var signed []byte
			sawInit := false
			for _, event := range events {
				if event.Type == "init" {
					sawInit = true
					if event.Catalog == nil || event.Catalog.Audio == nil {
						t.Fatal("missing audio catalog")
					}
					track := event.Catalog.Audio.Renditions["audio9"]
					if track.TrackID() != 9 || track.AlternateGroup != 3 || track.Label != "Music: 音楽" {
						t.Fatalf("unexpected audio catalog: %+v", track)
					}
				}
				if event.Type == "signed-segment" {
					signed = append(signed, event.Tracks["9"]...)
				}
			}
			if !sawInit || len(signed) == 0 {
				t.Fatal("missing initialization or signed audio")
			}
			result, err := eng.Verify(context.Background(), bytes.NewReader(signed))
			if err != nil {
				t.Fatal(err)
			}
			var report verifyDoc
			if err := json.Unmarshal([]byte(result), &report); err != nil || len(report.Segments) == 0 {
				t.Fatalf("invalid verification report: %v", err)
			}
			for _, segment := range report.Segments {
				if segment.ValidationState == "" || segment.ValidationState == "Invalid" {
					t.Fatalf("audio signature did not verify: %q", segment.ValidationState)
				}
			}
		})
	}
}

func TestCanonicalizePreservesAudioMetadata(t *testing.T) {
	eng := newEngine(t)
	output, err := eng.Canonicalize(context.Background(), readFile(t, fixtureFmp4),
		muxl.WithTrackRemap(map[uint32]uint32{2: 9}),
		muxl.WithTrackMetadata(map[uint32]muxl.TrackMeta{9: {AlternateGroup: 3, Label: "Music"}}))
	if err != nil {
		t.Fatal(err)
	}
	events, err := collectEvents(func(events chan<- *muxl.Event) error {
		return eng.UnwrapEvents(context.Background(), bytes.NewReader(output), events)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != "init" || event.Catalog == nil || event.Catalog.Audio == nil {
			continue
		}
		audio := event.Catalog.Audio.Renditions["audio9"]
		if audio.AlternateGroup != 3 || audio.Label != "Music" {
			t.Fatalf("canonical audio metadata = %+v", audio)
		}
		return
	}
	t.Fatal("canonical output has no audio catalog")
}
