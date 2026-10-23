package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"yt/internal/apperr"
)

func TestMCPInfoHandler(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := NewServer(f.client)
	res, _, err := s.getVideoInfo(context.Background(), nil, videoInfoIn{URLOrID: "testvideo01"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}
	out, ok := res.StructuredContent.(videoInfoOut)
	if !ok {
		t.Fatalf("want videoInfoOut, got %T", res.StructuredContent)
	}
	if !out.Captions.HasCaptions {
		t.Error("caption summary missing")
	}
	if out.ContentTrust != "untrusted" {
		t.Error("video info must be labelled untrusted")
	}
	raw := res.Content[0].(*mcpsdk.TextContent).Text
	if !strings.Contains(raw, `"content_trust":"untrusted"`) {
		t.Errorf("text block must carry the trust label: %q", raw)
	}
}

func TestMCPSearchHandler(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := NewServer(f.client)
	res, _, err := s.searchTranscript(context.Background(), nil, searchIn{URLOrID: "testvideo01", Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}
	out, ok := res.StructuredContent.(searchOut)
	if !ok {
		t.Fatalf("want searchOut, got %T", res.StructuredContent)
	}
	if out.TotalMatches != 1 {
		t.Fatalf("want 1 match, got %d", out.TotalMatches)
	}
}

func TestMCPTranscriptTruncationIsStructured(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := NewServer(f.client)
	maxChars := 200
	res, _, err := s.getTranscript(context.Background(), nil, transcriptIn{
		URLOrID: "testvideo01", MaxChars: &maxChars, Granularity: "cue", TimestampMode: "cue",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("unexpected tool error")
	}
	out, ok := res.StructuredContent.(transcriptOut)
	if !ok {
		t.Fatalf("want transcriptOut, got %T", res.StructuredContent)
	}
	meta := out.transcriptMeta
	if meta.ContentTrust != "untrusted" {
		t.Errorf("content must be labelled untrusted")
	}
	if !meta.Truncated || meta.NextCueIndex == nil || meta.NextStartSeconds == nil {
		t.Fatalf("expected structured truncation metadata, got %+v", meta)
	}
	body := res.Content[0].(*mcpsdk.TextContent).Text
	if !strings.Contains(body, "TRUNCATED") {
		t.Errorf("truncation must be visible at the top of the payload: %q", body)
	}
	if meta.ReturnedCues == 0 || meta.CueCount != 10 {
		t.Errorf("unexpected cue counts: returned=%d total=%d", meta.ReturnedCues, meta.CueCount)
	}
}

func TestMCPTranscriptContinuation(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := NewServer(f.client)
	maxChars := 200
	first, _, _ := s.getTranscript(context.Background(), nil, transcriptIn{
		URLOrID: "testvideo01", MaxChars: &maxChars, Granularity: "cue", TimestampMode: "cue",
	})
	meta := first.StructuredContent.(transcriptOut).transcriptMeta
	if meta.NextCueIndex == nil {
		t.Fatal("no continuation pointer")
	}
	second, _, _ := s.getTranscript(context.Background(), nil, transcriptIn{
		URLOrID: "testvideo01", MaxChars: &maxChars, Granularity: "cue", TimestampMode: "cue",
		StartCue: *meta.NextCueIndex,
	})
	meta2 := second.StructuredContent.(transcriptOut).transcriptMeta
	if meta2.ReturnedCues == 0 {
		t.Fatal("continuation returned nothing")
	}
	// Cue 0 is "Hello world"; cue i>=1 is "line <i> of speech". The
	// continuation must resume exactly at the reported pointer, so cue 0 must
	// be gone and the first line must match the pointer index.
	got := second.Content[0].(*mcpsdk.TextContent).Text
	if strings.Contains(got, "Hello world") {
		t.Errorf("continuation re-read the start instead of the pointer: %q", got)
	}
	want := fmt.Sprintf("line %d of speech", *meta.NextCueIndex)
	if !strings.Contains(got, want) {
		t.Errorf("continuation should start at cue %d (%q), got %q", *meta.NextCueIndex, want, got)
	}
}

func TestMCPErrorResultCarriesCode(t *testing.T) {
	s := NewServer(nil)
	res, _, err := s.getVideoInfo(context.Background(), nil, videoInfoIn{URLOrID: "not a url"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError result")
	}
	payload, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("want structured error payload, got %T", res.StructuredContent)
	}
	te, ok := payload["error"].(*apperr.Error)
	if !ok || te.Code != apperr.CodeInvalidID {
		t.Fatalf("want invalid_id, got %#v", payload["error"])
	}
}

func TestMCPListChannelVideos(t *testing.T) {
	f, cleanup := newFakeChannel(t)
	defer cleanup()
	f.feedXML = makeFeed("AI Engineer", 12)

	s := NewServer(f.client)
	res, _, err := s.listChannelVideos(context.Background(), nil, channelIn{Channel: "@aiDotEngineer"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}
	out, ok := res.StructuredContent.(channelListingOut)
	if !ok {
		t.Fatalf("want channelListingOut, got %T", res.StructuredContent)
	}
	listing := out.ChannelListing
	if listing.Count != 10 || !listing.Truncated || listing.Available != 12 {
		t.Fatalf("default limit: count=%d available=%d truncated=%v", listing.Count, listing.Available, listing.Truncated)
	}
	if out.ContentTrust != "untrusted" {
		t.Error("channel listing must be labelled untrusted")
	}
	tc, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok || tc.Text == "" {
		t.Fatalf("want a non-empty text block, got %#v", res.Content[0])
	}
	if !strings.Contains(tc.Text, `"content_trust":"untrusted"`) {
		t.Errorf("text block must carry the trust label: %q", tc.Text)
	}

	// limit=0 means all feed entries.
	zero := 0
	res, _, err = s.listChannelVideos(context.Background(), nil, channelIn{Channel: "@aiDotEngineer", Limit: &zero})
	if err != nil {
		t.Fatal(err)
	}
	listing = res.StructuredContent.(channelListingOut).ChannelListing
	if listing.Count != 12 || listing.Truncated {
		t.Fatalf("limit=0: count=%d truncated=%v", listing.Count, listing.Truncated)
	}

	// A bad since is a typed error result, not a Go error.
	res, _, err = s.listChannelVideos(context.Background(), nil, channelIn{Channel: "@aiDotEngineer", Since: "yesterday"})
	if err != nil {
		t.Fatal(err)
	}
	assertToolError(t, res, apperr.CodeBadRequest)
}
