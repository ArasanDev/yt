package search

import (
	"strings"
	"testing"

	"github.com/ArasanDev/yt/internal/transcript"
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

// Terms of three runes or fewer must match whole words: "cat" must not fire on
// "category".
func TestSearchShortTermMatchesWholeWordsOnly(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "the category page lists items"},
		{Start: 5, Text: "the cat sat on the mat"},
	}
	res := Segments(segs, Options{Query: "cat"})
	if res.TotalMatches != 1 || res.Matches[0].CueIndex != 1 {
		t.Fatalf("3-rune terms must match whole words only, got %+v", res.Matches)
	}
}

// Longer terms keep substring matching so stems keep working.
func TestSearchLongTermStillMatchesSubstrings(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "these skills help"}}
	res := Segments(segs, Options{Query: "skill"})
	if res.TotalMatches != 1 {
		t.Fatalf("4+ rune terms must keep substring matching for stems, got %d", res.TotalMatches)
	}
}

func TestSearchPunctuationOnlyQueryHasNote(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "hello world"}}
	res := Segments(segs, Options{Query: "?!?! -- ..."})
	if len(res.Matches) != 0 {
		t.Fatalf("punctuation-only query cannot match, got %+v", res.Matches)
	}
	if notes := strings.Join(res.Notes, " "); !strings.Contains(notes, "punctuation") {
		t.Errorf("punctuation-only query must explain itself, notes=%q", notes)
	}
}

func TestSearchStartCuePastEndHasNote(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "needle here"}, {Start: 1, Text: "more needle"}}
	res := Segments(segs, Options{Query: "needle", StartCue: 50})
	if len(res.Matches) != 0 {
		t.Fatalf("start_cue past the end cannot match, got %+v", res.Matches)
	}
	notes := strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "start_cue=50") || !strings.Contains(notes, "start_cue=0") {
		t.Errorf("start_cue past the end must be explained, notes=%q", notes)
	}
}

func TestSearchStartCuePastMatchesHasNote(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "needle here"}, {Start: 1, Text: "filler"}}
	res := Segments(segs, Options{Query: "needle", StartCue: 1})
	if len(res.Matches) != 0 {
		t.Fatalf("no matches expected after start_cue, got %+v", res.Matches)
	}
	notes := strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "start_cue=1") || !strings.Contains(notes, "earlier") {
		t.Errorf("matches skipped by start_cue must be explained, notes=%q", notes)
	}
}
