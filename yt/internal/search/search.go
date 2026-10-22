// Package search performs server-side transcript search: targeted retrieval
// instead of paging a 50KB blob into the agent's context.
package search

import (
	"fmt"
	"strings"
	"unicode"

	"yt/internal/transcript"
)

// Mode decides how a multi-word query is satisfied.
//
// All (default) preserves the original behaviour: every term must appear within
// the cue window. Any exists because a literal AND returns a false negative
// when the speaker paraphrases. Phrase requires adjacency.
type Mode string

const (
	All    Mode = "all"
	Any    Mode = "any"
	Phrase Mode = "phrase"
)

// Options are the search parameters resolved at the adapter boundary.
type Options struct {
	Query       string
	ContextCues int
	MaxMatches  int
	StartCue    int
	MatchMode   Mode
}

// Match is one matching cue with optional surrounding context.
type Match struct {
	CueIndex     int     `json:"cue_index"`
	StartSeconds float64 `json:"start_seconds"`
	Timestamp    string  `json:"timestamp"`
	Text         string  `json:"text"`
	Before       string  `json:"before,omitempty"`
	After        string  `json:"after,omitempty"`
}

// TermStat makes a zero result explainable: the agent can see that a term is
// absent from the transcript instead of guessing why nothing matched.
type TermStat struct {
	Term    string `json:"term"`
	Matches int    `json:"matches"`
}

// Result is the complete search response.
type Result struct {
	Matches      []Match `json:"matches"`
	TotalMatches int     `json:"total_matches"`
	Truncated    bool    `json:"truncated"`
	// NextCue is set when Truncated: pass it back as StartCue to continue.
	NextCue   *int       `json:"next_cue,omitempty"`
	MatchMode string     `json:"match_mode,omitempty"`
	Terms     []TermStat `json:"terms,omitempty"`
	Notes     []string   `json:"notes,omitempty"`
}

const (
	DefaultContextCues = 1
	DefaultMaxMatches  = 10
	MaxMatchesLimit    = 50

	maxContextCues = 10
	maxContextLen  = 2000 // runes
	// searchWindowCues tolerates auto-caption splitting: a phrase like
	// "unknown unknowns" is often two adjacent cues, and an AND over a single
	// cue would report a false negative.
	searchWindowCues   = 3
	maxWindowTextRunes = 500
)

// searchReplacer normalizes typography and hyphens on both sides of the match.
// Manual captions use curly apostrophes; agents type straight ones.
var searchReplacer = strings.NewReplacer(
	"’", "'", "‘", "'",
	"“", "\"", "”", "\"",
	"–", " ", "—", " ", "-", " ",
)

// NormalizeText lowercases and normalizes typography for matching and for
// advisory content checks.
func NormalizeText(s string) string {
	return strings.ToLower(searchReplacer.Replace(s))
}

// queryTerms splits a query into terms. A spelled-out acronym ("M C P") is
// collapsed, because no caption contains those as three separate words and
// matching each letter turns the query into noise.
func queryTerms(q string) []string {
	var out []string
	for _, t := range strings.Fields(NormalizeText(q)) {
		t = strings.Trim(t, ".,!?;:\"'()[]{}<>")
		if t != "" {
			out = append(out, t)
		}
	}
	if len(out) >= 2 && allSingleRune(out) {
		return []string{strings.Join(out, "")}
	}
	return out
}

func allSingleRune(terms []string) bool {
	for _, t := range terms {
		if len([]rune(t)) != 1 {
			return false
		}
	}
	return true
}

// containsTerm matches a token. Tokens of one or two runes ("ai", "5") are
// matched on word boundaries so they do not fire on every word containing
// those letters; longer tokens keep substring matching for stems like
// "evaluat" in "evaluation".
func containsTerm(haystack, term string) bool {
	if len([]rune(term)) > 2 {
		return strings.Contains(haystack, term)
	}
	h, t := []rune(haystack), []rune(term)
	for i := 0; i+len(t) <= len(h); i++ {
		if string(h[i:i+len(t)]) != term {
			continue
		}
		beforeOK := i == 0 || !isWordRune(h[i-1])
		afterOK := i+len(t) == len(h) || !isWordRune(h[i+len(t)])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func allTermsIn(haystack string, terms []string) bool {
	for _, t := range terms {
		if !containsTerm(haystack, t) {
			return false
		}
	}
	return true
}

// normalizePhrase keeps only letters and digits, so punctuation inserted by
// the caption stream ("hello, world") cannot break adjacency.
func normalizePhrase(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(searchReplacer.Replace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case !space:
			b.WriteRune(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func phraseIn(haystack, phrase string) bool {
	if phrase == "" {
		return false
	}
	return strings.Contains(" "+normalizePhrase(haystack)+" ", " "+phrase+" ")
}

func normalizeMatchMode(m Mode) Mode {
	mode, _ := ParseMode(string(m))
	return mode
}

// ParseMode reports whether the caller supplied a known mode, so a typo fails
// loudly instead of silently scanning with the default.
func ParseMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all":
		return All, true
	case "any":
		return Any, true
	case "phrase":
		return Phrase, true
	default:
		return All, false
	}
}

// Segments searches a transcript's cues.
func Segments(segs []transcript.Segment, opts Options) Result {
	mode := normalizeMatchMode(opts.MatchMode)
	terms := queryTerms(opts.Query)
	if len(terms) == 0 {
		return Result{Matches: []Match{}, MatchMode: string(mode)}
	}
	if opts.MaxMatches <= 0 {
		opts.MaxMatches = DefaultMaxMatches
	}
	if opts.MaxMatches > MaxMatchesLimit {
		opts.MaxMatches = MaxMatchesLimit
	}
	ctx := opts.ContextCues
	if ctx < 0 {
		ctx = 0
	}
	if ctx > maxContextCues {
		ctx = maxContextCues
	}

	norm := make([]string, len(segs))
	for i, s := range segs {
		norm[i] = NormalizeText(s.Text)
	}

	// Statistics cover the whole transcript, not the requested slice, so
	// "not found here" stays distinguishable from "not found at all".
	stats := make([]TermStat, len(terms))
	for ti, term := range terms {
		stats[ti] = TermStat{Term: term}
		for i := range norm {
			if containsTerm(norm[i], term) {
				stats[ti].Matches++
			}
		}
	}

	phrase := normalizePhrase(opts.Query)

	matchCue := func(i int) bool {
		switch mode {
		case Any:
			for _, term := range terms {
				if containsTerm(norm[i], term) {
					return true
				}
			}
			return false
		case Phrase:
			return phraseIn(segs[i].Text, phrase)
		default:
			return allTermsIn(norm[i], terms)
		}
	}

	exact := make([]bool, len(segs))
	for i := range segs {
		exact[i] = matchCue(i)
	}

	windowMatches := func(from, to int) bool {
		if mode == Phrase {
			return phraseIn(joinCueText(segs, from, to), phrase)
		}
		var w strings.Builder
		for j := from; j < to; j++ {
			w.WriteString(norm[j])
			w.WriteByte(' ')
		}
		return allTermsIn(w.String(), terms)
	}

	needsWindow := mode == Phrase || (mode == All && len(terms) > 1)

	res := Result{Matches: []Match{}, MatchMode: string(mode), Terms: stats}
	nextAfter := 0
	consider := func(anchor, spanStart, spanEnd int) {
		res.TotalMatches++
		if len(res.Matches) >= opts.MaxMatches {
			res.Truncated = true
			return
		}
		m := Match{
			CueIndex:     anchor,
			StartSeconds: segs[anchor].Start,
			Timestamp:    transcript.Timestamp(segs[anchor].Start),
		}
		if spanEnd == spanStart+1 {
			m.Text = segs[anchor].Text
		} else {
			text, _ := transcript.TruncateRunes(joinCueText(segs, spanStart, spanEnd), maxWindowTextRunes)
			m.Text = text
		}
		if ctx > 0 {
			m.Before = joinContext(segs, spanStart-ctx, spanStart)
			m.After = joinContext(segs, spanEnd, spanEnd+ctx)
		}
		res.Matches = append(res.Matches, m)
		nextAfter = spanEnd
	}

	startCue := opts.StartCue
	if startCue < 0 {
		startCue = 0
	}
	for i := startCue; i < len(segs); {
		if exact[i] {
			consider(i, i, i+1)
			i++
			continue
		}
		if needsWindow {
			end := i + searchWindowCues
			if end > len(segs) {
				end = len(segs)
			}
			covered := false
			for j := i; j < end; j++ {
				if exact[j] {
					covered = true
					break
				}
			}
			if !covered && windowMatches(i, end) {
				// Tighten to the cues that actually hold a term, so the
				// anchor and payload are not polluted by leading filler.
				first, last := end, -1
				for j := i; j < end; j++ {
					for _, term := range terms {
						if containsTerm(norm[j], term) {
							if j < first {
								first = j
							}
							if j > last {
								last = j
							}
							break
						}
					}
				}
				if first > last {
					first, last = i, end-1
				}
				consider(first, first, last+1)
				i = end
				continue
			}
		}
		i++
	}

	if res.Truncated {
		nc := nextAfter
		res.NextCue = &nc
		res.Notes = append(res.Notes, fmt.Sprintf("more matches follow; continue with start_cue=%d", nc))
	}

	if len(res.Matches) == 0 {
		var missing []string
		for _, s := range stats {
			if s.Matches == 0 {
				missing = append(missing, s.Term)
			}
		}
		if len(missing) > 0 {
			res.Notes = append(res.Notes, "not found in transcript: "+strings.Join(missing, ", ")+
				" (auto-captions may spell names differently)")
		}
		if mode == All && len(terms) > 1 && len(missing) == 0 {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"the %d terms never co-occur within %d cues; retry with match_mode=any",
				len(terms), searchWindowCues))
		}
		if mode == Phrase && len(missing) == 0 {
			res.Notes = append(res.Notes, "phrase mode needs adjacent words; retry with match_mode=all or any for paraphrases")
		}
	}
	return res
}

func joinCueText(segs []transcript.Segment, from, to int) string {
	var parts []string
	for i := from; i < to && i < len(segs); i++ {
		parts = append(parts, segs[i].Text)
	}
	return strings.Join(parts, " ")
}

func joinContext(segs []transcript.Segment, from, to int) string {
	if from < 0 {
		from = 0
	}
	if to > len(segs) {
		to = len(segs)
	}
	if from >= to {
		return ""
	}
	joined := joinCueText(segs, from, to)
	out, _ := transcript.TruncateRunes(joined, maxContextLen)
	return out
}
