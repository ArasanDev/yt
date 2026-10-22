package search

import (
	"testing"

	"yt/internal/transcript"
)

func TestSearchSegments(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "welcome to the show"},
		{Start: 10, Text: "today we talk about domain driven design"},
		{Start: 20, Text: "domain driven design is a technique"},
		{Start: 30, Text: "unrelated filler"},
	}
	res := Segments(segs, Options{Query: "domain driven", ContextCues: 1, MaxMatches: 10})
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
	segs := []transcript.Segment{{Start: 0, Text: "domain design"}, {Start: 1, Text: "domain driven design"}}
	res := Segments(segs, Options{Query: "domain driven"})
	if res.TotalMatches != 1 {
		t.Fatalf("all terms must be required; got %d", res.TotalMatches)
	}
}

// Regression: auto-captions split phrases across adjacent cues; an AND over a
// single cue reported a false negative for the topic.
func TestSearchMatchesAcrossAdjacentCues(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "the skill is being aware of unknown"},
		{Start: 2, Text: "unknowns are real, right? could be security"},
		{Start: 4, Text: "unrelated filler"},
	}
	res := Segments(segs, Options{Query: "aware real"})
	if res.TotalMatches != 1 {
		t.Fatalf("terms split across cues should match once, got %d", res.TotalMatches)
	}
	if res.Matches[0].CueIndex != 0 {
		t.Errorf("match should anchor on the earliest cue holding the first term, got cue %d", res.Matches[0].CueIndex)
	}
}

func TestSearchWindowIsBounded(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "alpha"}, {Start: 1, Text: "filler"}, {Start: 2, Text: "filler"}, {Start: 3, Text: "filler"}, {Start: 9, Text: "beta"}}
	res := Segments(segs, Options{Query: "alpha beta"})
	if res.TotalMatches != 0 {
		t.Fatalf("terms far apart must not match, got %d", res.TotalMatches)
	}
}

func TestSearchSingleTermIsPerCue(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "skill"}, {Start: 1, Text: "skill"}, {Start: 2, Text: "skill"}}
	res := Segments(segs, Options{Query: "skill"})
	if res.TotalMatches != 3 {
		t.Fatalf("single-term search must stay per-cue, got %d", res.TotalMatches)
	}
}

func TestSearchTruncationHasContinuation(t *testing.T) {
	var segs []transcript.Segment
	for i := 0; i < 12; i++ {
		segs = append(segs, transcript.Segment{Start: float64(i), Text: "needle here"})
	}
	first := Segments(segs, Options{Query: "needle", MaxMatches: 5})
	if !first.Truncated || first.NextCue == nil || *first.NextCue != 5 {
		t.Fatalf("expected resumable truncation, got %+v", first)
	}
	second := Segments(segs, Options{Query: "needle", MaxMatches: 5, StartCue: *first.NextCue})
	if len(second.Matches) != 5 || second.Matches[0].CueIndex != 5 {
		t.Fatalf("continuation did not resume at the pointer: %+v", second.Matches)
	}
}

func TestSearchTruncatesMatches(t *testing.T) {
	var segs []transcript.Segment
	for i := 0; i < 20; i++ {
		segs = append(segs, transcript.Segment{Start: float64(i), Text: "needle here"})
	}
	res := Segments(segs, Options{Query: "needle", MaxMatches: 5})
	if len(res.Matches) != 5 || !res.Truncated || res.TotalMatches != 20 {
		t.Fatalf("unexpected: matches=%d truncated=%v total=%d", len(res.Matches), res.Truncated, res.TotalMatches)
	}
}

// Regression: finding one exact cue must not suppress split-phrase hits
// elsewhere in the transcript.
func TestSearchAddsSplitHitsWhenExactExists(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "alpha beta"},
		{Start: 5, Text: "filler"},
		{Start: 9, Text: "filler"},
		{Start: 10, Text: "alpha"},
		{Start: 11, Text: "beta"},
	}
	res := Segments(segs, Options{Query: "alpha beta", MaxMatches: 10})
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
	segs := []transcript.Segment{{Start: 0, Text: "we don’t need state-of-the-art tools"}}
	res := Segments(segs, Options{Query: "don't state of the art"})
	if res.TotalMatches != 1 {
		t.Fatalf("curly apostrophe/hyphen normalization failed: %d", res.TotalMatches)
	}
}
