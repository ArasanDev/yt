package mcp

import (
	"context"
	"testing"

	"yt/internal/render"
	"yt/internal/search"
)

// timestamp_window_seconds must describe the rendered timestamp's precision,
// which follows timestamp_mode, not granularity: block-mode stamps cover a
// merged ~30s span, cue-mode stamps are exact line starts, none has no stamps.
func TestMCPTimestampWindowFollowsTimestampMode(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := NewServer(f.client)

	cases := []struct {
		name        string
		granularity string
		timestamps  bool // legacy alias
		mode        string
		wantWindow  int
	}{
		{"defaults", "", false, "", int(render.BlockSeconds)},
		{"block/block", "block", false, "block", int(render.BlockSeconds)},
		{"block/cue", "block", false, "cue", 0},
		{"block/none", "block", false, "none", 0},
		{"cue/block", "cue", false, "block", int(render.BlockSeconds)},
		{"cue/cue", "cue", false, "cue", 0},
		{"cue/none", "cue", false, "none", 0},
		{"default/none", "", false, "none", 0},
		{"legacy timestamps=true", "", true, "", 0},
		{"legacy timestamps + explicit mode wins", "", true, "none", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _, err := s.getTranscript(context.Background(), nil, transcriptIn{
				URLOrID: "testvideo01", Granularity: tc.granularity,
				TimestampMode: tc.mode, Timestamps: tc.timestamps,
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatalf("unexpected tool error: %v", res.Content)
			}
			out := res.StructuredContent.(transcriptOut)
			wantMode := tc.mode
			if wantMode == "" {
				if tc.timestamps {
					wantMode = "cue"
				} else {
					wantMode = "block"
				}
			}
			if out.TimestampMode != wantMode {
				t.Errorf("timestamp_mode = %q, want %q", out.TimestampMode, wantMode)
			}
			if out.TimestampWindowSeconds != tc.wantWindow {
				t.Errorf("timestamp_window_seconds = %d, want %d (mode=%q)",
					out.TimestampWindowSeconds, tc.wantWindow, out.TimestampMode)
			}
		})
	}
}

func TestMCPJSONTimestampWindowIsZero(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := NewServer(f.client)

	res, _, err := s.getTranscript(context.Background(), nil, transcriptIn{URLOrID: "testvideo01", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	out := res.StructuredContent.(transcriptOut)
	if out.TimestampWindowSeconds != 0 {
		t.Errorf("JSON returns exact cue starts, window should be 0, got %d", out.TimestampWindowSeconds)
	}
	if out.Granularity != "cue" {
		t.Errorf("granularity = %q, want cue", out.Granularity)
	}
}

func TestMCPSearchRejectsUnknownMatchMode(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := NewServer(f.client)

	res, _, err := s.searchTranscript(context.Background(), nil, searchIn{
		URLOrID:   "testvideo01",
		Query:     "hello",
		MatchMode: "fuzzy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("unknown match_mode must be a tool error, got %+v", res.StructuredContent)
	}
}

func TestMCPSearchPassesMatchMode(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := NewServer(f.client)

	res, _, err := s.searchTranscript(context.Background(), nil, searchIn{
		URLOrID:   "testvideo01",
		Query:     "hello world",
		MatchMode: "phrase",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.StructuredContent.(searchOut)
	if !ok {
		t.Fatalf("want searchOut, got %T", res.StructuredContent)
	}
	if out.MatchMode != string(search.Phrase) {
		t.Errorf("match_mode = %q, want phrase", out.MatchMode)
	}
	if out.TotalMatches != 1 {
		t.Errorf("want 1 phrase match, got %+v", out)
	}
}
