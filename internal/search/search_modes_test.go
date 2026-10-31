package search

import (
	"strings"
	"testing"

	"github.com/ArasanDev/yt/internal/transcript"
)

func TestSearchMatchAny(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "alpha only"},
		{Start: 10, Text: "filler"},
		{Start: 20, Text: "filler"},
		{Start: 30, Text: "filler"},
		{Start: 40, Text: "beta only"},
	}

	all := Segments(segs, Options{Query: "alpha beta"})
	if all.TotalMatches != 0 {
		t.Fatalf("AND mode should not span distant cues, got %d", all.TotalMatches)
	}

	any := Segments(segs, Options{Query: "alpha beta", MatchMode: Any})
	if any.TotalMatches != 2 {
		t.Fatalf("any mode should match each term, got %d", any.TotalMatches)
	}
	if any.MatchMode != string(Any) {
		t.Errorf("match_mode = %q, want %q", any.MatchMode, Any)
	}
}

func TestSearchMatchPhraseRequiresAdjacency(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "we care about state of the art models"},
		{Start: 10, Text: "the art of state management"},
	}
	res := Segments(segs, Options{Query: "state of the art", MatchMode: Phrase})
	if res.TotalMatches != 1 || res.Matches[0].CueIndex != 0 {
		t.Fatalf("phrase mode should only match cue 0, got %+v", res.Matches)
	}
}

func TestSearchPhraseSpansAdjacentCues(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "the real question is unknown"},
		{Start: 3, Text: "unknowns and how we find them"},
	}
	res := Segments(segs, Options{Query: "unknown unknowns", MatchMode: Phrase})
	if res.TotalMatches != 1 {
		t.Fatalf("phrase split across two cues should match once, got %d", res.TotalMatches)
	}
}

func TestSearchCollapsesSpelledAcronym(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "the MCP server exposes tools"}}
	res := Segments(segs, Options{Query: "M C P"})
	if res.TotalMatches != 1 {
		t.Fatalf("spelled acronym should collapse to mcp, got %d matches; terms=%+v", res.TotalMatches, res.Terms)
	}
	if len(res.Terms) != 1 || res.Terms[0].Term != "mcp" {
		t.Errorf("terms = %+v, want one term mcp", res.Terms)
	}
}

// A contiguous run of single-letter tokens inside a larger query must collapse
// too, not only when the whole query is the acronym.
func TestSearchCollapsesAcronymRunInsideQuery(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "the mcp server exposes tools"}}
	res := Segments(segs, Options{Query: "M C P server"})
	if res.TotalMatches != 1 {
		t.Fatalf("acronym run inside a query should collapse, got %d matches; terms=%+v", res.TotalMatches, res.Terms)
	}
	if len(res.Terms) != 2 || res.Terms[0].Term != "mcp" || res.Terms[1].Term != "server" {
		t.Errorf("terms = %+v, want [mcp server]", res.Terms)
	}
}

// Ordinary short words ("ai", "ml") are not acronym letters and must not be
// glued into nonsense.
func TestSearchDoesNotCollapseOrdinaryShortWords(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "ai and ml together"}}
	res := Segments(segs, Options{Query: "ai ml"})
	if len(res.Terms) != 2 || res.Terms[0].Term != "ai" || res.Terms[1].Term != "ml" {
		t.Fatalf("two-letter words must not collapse into an acronym, terms=%+v", res.Terms)
	}
	if res.TotalMatches != 1 {
		t.Fatalf("both terms exist in one cue, got %d matches", res.TotalMatches)
	}
}

func TestSearchDoesNotCollapseTwoSingleLetters(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "a b testing"}}
	res := Segments(segs, Options{Query: "a b"})
	if len(res.Terms) != 2 || res.Terms[0].Term != "a" || res.Terms[1].Term != "b" {
		t.Fatalf("two single-letter words must not collapse into an acronym, terms=%+v", res.Terms)
	}
}

func TestSearchShortTermUsesWordBoundary(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "she said the chain is fine"},
		{Start: 10, Text: "an ai agent arrived"},
	}
	res := Segments(segs, Options{Query: "ai"})
	if res.TotalMatches != 1 || res.Matches[0].CueIndex != 1 {
		t.Fatalf("short terms must match whole words only, got %+v", res.Matches)
	}
}

func TestSearchZeroReportsTermsAndSuggestsAny(t *testing.T) {
	segs := []transcript.Segment{
		{Start: 0, Text: "alpha only"},
		{Start: 10, Text: "filler"},
		{Start: 20, Text: "filler"},
		{Start: 30, Text: "filler"},
		{Start: 40, Text: "beta only"},
	}
	res := Segments(segs, Options{Query: "alpha beta"})
	if len(res.Terms) != 2 || res.Terms[0].Matches != 1 || res.Terms[1].Matches != 1 {
		t.Fatalf("per-term stats should show both terms exist: %+v", res.Terms)
	}
	notes := strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "match_mode=any") {
		t.Errorf("zero co-occurrence should suggest any mode, notes=%q", notes)
	}
}

func TestSearchZeroNamesMissingTerm(t *testing.T) {
	segs := []transcript.Segment{{Start: 0, Text: "alpha beta"}}
	res := Segments(segs, Options{Query: "alpha delta"})
	notes := strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "delta") {
		t.Errorf("missing term should be named, notes=%+v", res.Notes)
	}
	if strings.Contains(notes, "match_mode=any") {
		t.Errorf("a missing term is not fixed by any mode, notes=%+v", res.Notes)
	}
}
