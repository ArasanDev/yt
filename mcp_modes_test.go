package main

import (
	"context"
	"testing"
)

func TestInfoParsesChaptersPastDescriptionCutoff(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	f.description = "Blurb.\n\nChapters:\n00:00 Opening\n00:10 Middle\n00:20 End\n\nLinks:\nhttps://example.com\n"

	info, err := NewClient().Info(context.Background(), "testvideo01", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Chapters) != 3 {
		t.Fatalf("want 3 chapters even when the description is truncated, got %+v", info.Chapters)
	}
	if info.Chapters[1].StartSeconds != 10 || info.Chapters[1].Title != "Middle" {
		t.Errorf("chapter 1 = %+v", info.Chapters[1])
	}
	if !info.DescriptionTruncated {
		t.Error("description should still be truncated for the payload")
	}
}

func TestMCPTimestampWindowMatchesGranularity(t *testing.T) {
	_, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := &MCPServer{client: NewClient()}

	block, _, err := s.getTranscript(context.Background(), nil, transcriptIn{URLOrID: "testvideo01", Granularity: "block"})
	if err != nil {
		t.Fatal(err)
	}
	bout := block.StructuredContent.(transcriptOut)
	if bout.TimestampWindowSeconds != int(blockSeconds) {
		t.Errorf("block window = %d, want %d", bout.TimestampWindowSeconds, int(blockSeconds))
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
	_, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := &MCPServer{client: NewClient()}

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
	_, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := &MCPServer{client: NewClient()}

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
	_, cleanup := newFakeYouTube(t)
	defer cleanup()
	s := &MCPServer{client: NewClient()}

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
	if out.MatchMode != string(MatchPhrase) {
		t.Errorf("match_mode = %q, want phrase", out.MatchMode)
	}
	if out.TotalMatches != 1 {
		t.Errorf("want 1 phrase match, got %+v", out)
	}
}
