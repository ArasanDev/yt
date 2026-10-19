package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSearchSegments(t *testing.T) {
	segs := []Segment{
		{Start: 0, Text: "welcome to the show"},
		{Start: 10, Text: "today we talk about domain driven design"},
		{Start: 20, Text: "domain driven design is a technique"},
		{Start: 30, Text: "unrelated filler"},
	}
	res := searchSegments(segs, SearchOptions{Query: "domain driven", ContextCues: 1, MaxMatches: 10})
	if res.TotalMatches != 2 {
		t.Fatalf("want 2 matches, got %d", res.TotalMatches)
	}
	if res.Matches[0].CueIndex != 1 || res.Matches[0].Timestamp != "00:10" {
		t.Errorf("unexpected first match: %+v", res.Matches[0])
	}
	if res.Matches[0].Before == "" || res.Matches[0].After == "" {
		t.Errorf("expected context around match: %+v", res.Matches[0])
	}
}

func TestSearchRequiresAllTerms(t *testing.T) {
	segs := []Segment{{Start: 0, Text: "domain design"}, {Start: 1, Text: "domain driven design"}}
	res := searchSegments(segs, SearchOptions{Query: "domain driven"})
	if res.TotalMatches != 1 {
		t.Fatalf("all terms must be required; got %d", res.TotalMatches)
	}
}

// Regression: auto-captions split phrases across adjacent cues; an AND over a
// single cue reported a false negative for the topic.
func TestSearchMatchesAcrossAdjacentCues(t *testing.T) {
	segs := []Segment{
		{Start: 0, Text: "the skill is being aware of unknown"},
		{Start: 2, Text: "unknowns are real, right? could be security"},
		{Start: 4, Text: "unrelated filler"},
	}
	res := searchSegments(segs, SearchOptions{Query: "aware real"})
	if res.TotalMatches != 1 {
		t.Fatalf("terms split across cues should match once, got %d", res.TotalMatches)
	}
	if res.Matches[0].CueIndex != 0 {
		t.Errorf("match should anchor on the earliest cue holding the first term, got cue %d", res.Matches[0].CueIndex)
	}
}

func TestSearchWindowIsBounded(t *testing.T) {
	segs := []Segment{{Start: 0, Text: "alpha"}, {Start: 1, Text: "filler"}, {Start: 2, Text: "filler"}, {Start: 3, Text: "filler"}, {Start: 9, Text: "beta"}}
	res := searchSegments(segs, SearchOptions{Query: "alpha beta"})
	if res.TotalMatches != 0 {
		t.Fatalf("terms far apart must not match, got %d", res.TotalMatches)
	}
}

func TestSearchSingleTermIsPerCue(t *testing.T) {
	segs := []Segment{{Start: 0, Text: "skill"}, {Start: 1, Text: "skill"}, {Start: 2, Text: "skill"}}
	res := searchSegments(segs, SearchOptions{Query: "skill"})
	if res.TotalMatches != 3 {
		t.Fatalf("single-term search must stay per-cue, got %d", res.TotalMatches)
	}
}

func TestSearchTruncationHasContinuation(t *testing.T) {
	var segs []Segment
	for i := 0; i < 12; i++ {
		segs = append(segs, Segment{Start: float64(i), Text: "needle here"})
	}
	first := searchSegments(segs, SearchOptions{Query: "needle", MaxMatches: 5})
	if !first.Truncated || first.NextCue == nil || *first.NextCue != 5 {
		t.Fatalf("expected resumable truncation, got %+v", first)
	}
	second := searchSegments(segs, SearchOptions{Query: "needle", MaxMatches: 5, StartCue: *first.NextCue})
	if len(second.Matches) != 5 || second.Matches[0].CueIndex != 5 {
		t.Fatalf("continuation did not resume at the pointer: %+v", second.Matches)
	}
}

func TestSearchTruncatesMatches(t *testing.T) {
	var segs []Segment
	for i := 0; i < 20; i++ {
		segs = append(segs, Segment{Start: float64(i), Text: "needle here"})
	}
	res := searchSegments(segs, SearchOptions{Query: "needle", MaxMatches: 5})
	if len(res.Matches) != 5 || !res.Truncated || res.TotalMatches != 20 {
		t.Fatalf("unexpected: matches=%d truncated=%v total=%d", len(res.Matches), res.Truncated, res.TotalMatches)
	}
}

func TestMCPInfoHandler(t *testing.T) {
	_, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := &MCPServer{client: NewClient()}
	res, _, err := s.getVideoInfo(context.Background(), nil, videoInfoIn{URLOrID: "testvideo01"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}
	info, ok := res.StructuredContent.(*VideoInfo)
	if !ok {
		t.Fatalf("want *VideoInfo, got %T", res.StructuredContent)
	}
	if !info.Captions.HasCaptions {
		t.Error("caption summary missing")
	}
}

func TestMCPSearchHandler(t *testing.T) {
	_, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := &MCPServer{client: NewClient()}
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
	_, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := &MCPServer{client: NewClient()}
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
	body := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(body, "TRUNCATED") {
		t.Errorf("truncation must be visible at the top of the payload: %q", body)
	}
	if meta.ReturnedCues == 0 || meta.CueCount != 10 {
		t.Errorf("unexpected cue counts: returned=%d total=%d", meta.ReturnedCues, meta.CueCount)
	}
}

func TestMCPTranscriptContinuation(t *testing.T) {
	_, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := &MCPServer{client: NewClient()}
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
	got := second.Content[0].(*mcp.TextContent).Text
	if strings.Contains(got, "Hello world") {
		t.Errorf("continuation re-read the start instead of the pointer: %q", got)
	}
	want := fmt.Sprintf("line %d of speech", *meta.NextCueIndex)
	if !strings.Contains(got, want) {
		t.Errorf("continuation should start at cue %d (%q), got %q", *meta.NextCueIndex, want, got)
	}
}

// Regression: finding one exact cue must not suppress split-phrase hits
// elsewhere in the transcript.
func TestSearchAddsSplitHitsWhenExactExists(t *testing.T) {
	segs := []Segment{
		{Start: 0, Text: "alpha beta"},
		{Start: 5, Text: "filler"},
		{Start: 9, Text: "filler"},
		{Start: 10, Text: "alpha"},
		{Start: 11, Text: "beta"},
	}
	res := searchSegments(segs, SearchOptions{Query: "alpha beta", MaxMatches: 10})
	if res.TotalMatches != 2 {
		t.Fatalf("want exact + split match, got %d", res.TotalMatches)
	}
	if res.Matches[0].CueIndex != 0 || res.Matches[1].CueIndex != 3 {
		t.Errorf("unexpected anchors: %d, %d", res.Matches[0].CueIndex, res.Matches[1].CueIndex)
	}
	if res.Matches[1].Text != "alpha beta" {
		t.Errorf("split match must expose both terms, got %q", res.Matches[1].Text)
	}
}

func TestSearchNormalizesTypographyAndHyphens(t *testing.T) {
	segs := []Segment{{Start: 0, Text: "we don’t need state-of-the-art tools"}}
	res := searchSegments(segs, SearchOptions{Query: "don't state of the art"})
	if res.TotalMatches != 1 {
		t.Fatalf("curly apostrophe/hyphen normalization failed: %d", res.TotalMatches)
	}
}

func TestMCPErrorResultCarriesCode(t *testing.T) {
	s := &MCPServer{client: NewClient()}
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
	te, ok := payload["error"].(*ToolError)
	if !ok || te.Code != CodeInvalidID {
		t.Fatalf("want invalid_id, got %#v", payload["error"])
	}
}
