package mcp

import (
	"context"
	"testing"

	"yt/internal/render"
	"yt/internal/search"
)

func TestMCPTimestampWindowMatchesGranularity(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := NewServer(f.client)

	block, _, err := s.getTranscript(context.Background(), nil, transcriptIn{URLOrID: "testvideo01", Granularity: "block"})
	if err != nil {
		t.Fatal(err)
	}
	bout := block.StructuredContent.(transcriptOut)
	if bout.TimestampWindowSeconds != int(render.BlockSeconds) {
		t.Errorf("block window = %d, want %d", bout.TimestampWindowSeconds, int(render.BlockSeconds))
	}

	cue, _, err := s.getTranscript(context.Background(), nil, transcriptIn{URLOrID: "testvideo01", Granularity: "cue", TimestampMode: "cue"})
	if err != nil {
		t.Fatal(err)
	}
	cout := cue.StructuredContent.(transcriptOut)
	if cout.TimestampWindowSeconds != 0 {
		t.Errorf("cue window = %d, want 0", cout.TimestampWindowSeconds)
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
