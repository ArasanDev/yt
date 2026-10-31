// Package render turns transcript segments into agent-sized text with
// structural truncation metadata. Truncation state is reported in the header
// and in structured metadata, never only in a trailing footer (clients cut the
// tail first).
package render

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ArasanDev/yt/internal/transcript"
)

const (
	// BlockSeconds is the granularity=block window, also reported as the
	// timestamp precision in structured metadata.
	BlockSeconds = 30.0
	blockGap     = 2.5 // seconds of silence that also starts a new block
)

// Options control range selection and rendering.
type Options struct {
	Granularity   string  // cue | block
	TimestampMode string  // none | block | cue
	StartSeconds  float64 // 0 = from beginning
	EndSeconds    float64 // 0 = to end
	StartCue      int
	MaxCues       int
	MaxChars      int  // 0 = unlimited; bounds the body only
	Bare          bool // text only: omit the metadata header and truncation trailer
}

// Rendered is the text rendering plus its truncation metadata.
type Rendered struct {
	Text             string
	Granularity      string
	TimestampMode    string
	Notes            []string
	TotalCues        int
	ReturnedCues     int
	TotalChars       int // runes, whole transcript
	ReturnedChars    int // runes, returned body only
	Words            int // whole transcript
	ReturnedWords    int
	StartSeconds     float64
	EndSeconds       float64
	Truncated        bool
	NextStartSeconds *float64
	NextCueIndex     *int
	Coverage         string
}

// JSONSelection is the cue-level counterpart of Rendered: the same selection
// and char budget, but expressed as raw segments so meta and payload agree.
type JSONSelection struct {
	Segments         []transcript.Segment
	ReturnedCues     int
	ReturnedWords    int
	TotalChars       int
	ReturnedChars    int
	Truncated        bool
	NextStartSeconds *float64
	NextCueIndex     *int
	Coverage         string
}

type idxSeg struct {
	i   int
	seg transcript.Segment
}

type renderBlock struct {
	Start    float64
	End      float64
	Text     string
	FirstCue int
	CueCount int
}

func normalizeGranularity(g string) (string, []string) {
	switch strings.ToLower(strings.TrimSpace(g)) {
	case "cue":
		return "cue", nil
	case "", "block", "paragraph":
		return "block", nil
	case "sentence":
		return "block", []string{"granularity=sentence is not reliable on auto-generated captions (no punctuation); returned block granularity instead."}
	default:
		return "block", []string{fmt.Sprintf("unknown granularity %q; used block", g)}
	}
}

func normalizeTimestampMode(m string, legacyTimestamps bool) (string, []string) {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "none":
		return "none", nil
	case "cue":
		return "cue", nil
	case "block":
		return "block", nil
	case "":
		if legacyTimestamps {
			return "cue", nil
		}
		return "block", nil
	default:
		return "block", []string{fmt.Sprintf("unknown timestamp_mode %q; used block", m)}
	}
}

// selectSegments returns the cue slice for the requested range. cappedNext is
// non-nil exactly when MaxCues cut cues out of the requested range (not just
// the end of the transcript); it points at the index of the first excluded
// cue, which resumes correctly via StartCue or StartSeconds.
func selectSegments(segs []transcript.Segment, opts Options) (sel []idxSeg, cappedNext *int) {
	start := 0
	if opts.StartCue > 0 {
		start = opts.StartCue
	}
	if opts.StartSeconds > 0 {
		i := 0
		for i < len(segs) && segs[i].Start < opts.StartSeconds {
			i++
		}
		if i > start {
			start = i
		}
	}
	if start > len(segs) {
		start = len(segs)
	}
	end := len(segs)
	if opts.EndSeconds > 0 {
		e := start
		for e < len(segs) && segs[e].Start < opts.EndSeconds {
			e++
		}
		end = e
	}
	if opts.MaxCues > 0 && start+opts.MaxCues < end {
		end = start + opts.MaxCues
		i := end
		cappedNext = &i
	}
	out := make([]idxSeg, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, idxSeg{i: i, seg: segs[i]})
	}
	return out, cappedNext
}

// Select returns the raw segments in the requested range. It is used by SRT
// output, where the rendered text blocks are not wanted.
func Select(segs []transcript.Segment, opts Options) []transcript.Segment {
	sel, _ := selectSegments(segs, opts)
	out := make([]transcript.Segment, 0, len(sel))
	for _, s := range sel {
		out = append(out, s.seg)
	}
	return out
}

func buildBlocks(sel []idxSeg, granularity string) []renderBlock {
	var blocks []renderBlock
	for _, is := range sel {
		s := is.seg
		if len(blocks) == 0 || granularity == "cue" {
			blocks = append(blocks, renderBlock{
				Start: s.Start, End: s.Start + s.Duration, Text: s.Text, FirstCue: is.i, CueCount: 1,
			})
			continue
		}
		cur := &blocks[len(blocks)-1]
		if s.Start-cur.Start >= BlockSeconds || s.Start-cur.End > blockGap {
			blocks = append(blocks, renderBlock{
				Start: s.Start, End: s.Start + s.Duration, Text: s.Text, FirstCue: is.i, CueCount: 1,
			})
			continue
		}
		cur.Text += " " + s.Text
		if e := s.Start + s.Duration; e > cur.End {
			cur.End = e
		}
		cur.CueCount++
	}
	return blocks
}

func renderLine(blk renderBlock, mode string, nextStamp *float64) string {
	switch mode {
	case "cue":
		return "[" + transcript.Timestamp(blk.Start) + "] " + blk.Text
	case "block":
		if blk.Start >= *nextStamp {
			*nextStamp = blk.Start + BlockSeconds
			return "[" + transcript.Timestamp(blk.Start) + "] " + blk.Text
		}
	}
	return blk.Text
}

// transcriptHeader deliberately reports returned/total pairs. The header is
// the part a client always keeps, so "cues=2/1346" prevents an agent from
// mistaking a sliced result for the whole transcript.
func transcriptHeader(t *transcript.Transcript, durationSeconds, returnedCues, totalCues, returnedWords, totalWords int) string {
	parts := []string{
		"# transcript",
		"video=" + t.VideoID,
		"language=" + t.Language,
		"original_language=" + t.OriginalLanguage,
		fmt.Sprintf("translated=%v", t.Translated),
		"source=" + describeSource(t.Source),
		fmt.Sprintf("generated=%v", t.Generated),
		fmt.Sprintf("cues=%d/%d", returnedCues, totalCues),
		fmt.Sprintf("words=%d/%d", returnedWords, totalWords),
		"content=untrusted",
	}
	if durationSeconds > 0 {
		parts = append(parts, "duration="+transcript.Timestamp(float64(durationSeconds)))
	}
	return strings.Join(parts, " | ")
}

func describeSource(via string) string {
	if via == "" {
		return "unknown"
	}
	return via
}

func canonicalChars(segs []transcript.Segment) int {
	n := 0
	for _, s := range segs {
		n += utf8.RuneCountInString(s.Text) + 1
	}
	return n
}

// Render renders a transcript as text.
func Render(t *transcript.Transcript, durationSeconds int, opts Options) Rendered {
	gran, notes := normalizeGranularity(opts.Granularity)
	mode, modeNotes := normalizeTimestampMode(opts.TimestampMode, false)
	notes = append(notes, modeNotes...)
	r := Rendered{
		Granularity:   gran,
		TimestampMode: mode,
		Notes:         notes,
		TotalCues:     len(t.Segments),
		TotalChars:    canonicalChars(t.Segments),
		Words:         transcript.WordCount(t.Segments),
	}
	sel, cappedNext := selectSegments(t.Segments, opts)
	blocks := buildBlocks(sel, gran)

	var body strings.Builder
	nextStamp := 0.0
	var lastEnd float64
	budget := 0
	for _, blk := range blocks {
		line := renderLine(blk, mode, &nextStamp)
		lineRunes := utf8.RuneCountInString(line)
		if opts.MaxChars > 0 && r.ReturnedCues > 0 && budget+lineRunes+1 > opts.MaxChars {
			r.Truncated = true
			ns := blk.Start
			nc := blk.FirstCue
			r.NextStartSeconds = &ns
			r.NextCueIndex = &nc
			break
		}
		if r.ReturnedCues == 0 {
			r.StartSeconds = blk.Start
		}
		body.WriteString(line)
		body.WriteString("\n")
		budget += lineRunes + 1
		lastEnd = blk.End
		r.ReturnedCues += blk.CueCount
		r.ReturnedWords += len(strings.Fields(blk.Text))
	}

	if r.ReturnedCues == 0 {
		r.StartSeconds = opts.StartSeconds
		r.EndSeconds = opts.StartSeconds
		r.Notes = append(r.Notes, "no cues in the requested range")
	} else {
		r.EndSeconds = lastEnd
	}

	// max_cues is also a truncation: more cues remain in the requested range
	// (possibly bounded by end_seconds, not just the transcript end) and the
	// agent must be told, otherwise it presents its own cap as the end of the
	// video.
	if !r.Truncated && cappedNext != nil && len(sel) > 0 {
		r.Truncated = true
		ns := t.Segments[*cappedNext].Start
		nc := *cappedNext
		r.NextStartSeconds = &ns
		r.NextCueIndex = &nc
	}

	r.Coverage = fmt.Sprintf("%s-%s of %s",
		transcript.Timestamp(r.StartSeconds), transcript.Timestamp(r.EndSeconds), transcript.Timestamp(float64(durationSeconds)))

	header := transcriptHeader(t, durationSeconds, r.ReturnedCues, r.TotalCues, r.ReturnedWords, r.Words)
	if r.Truncated && r.NextStartSeconds != nil && r.NextCueIndex != nil {
		// Exact seconds: rounding up here would skip the boundary cue when the
		// agent resumes with this value.
		header += fmt.Sprintf(" | TRUNCATED next_start_seconds=%s next_cue_index=%d",
			formatSeconds(*r.NextStartSeconds), *r.NextCueIndex)
	}

	bodyText := strings.TrimRight(body.String(), "\n")
	r.ReturnedChars = utf8.RuneCountInString(bodyText)
	if opts.Bare {
		// The caller opted out of the header and trailer. Truncation metadata
		// stays on the struct so the calling surface can still report it (the
		// CLI writes it to stderr and never leaves a bare result silently cut).
		r.Text = bodyText
		return r
	}
	var out strings.Builder
	out.WriteString(header)
	out.WriteString("\n\n")
	out.WriteString(bodyText)
	if r.Truncated && r.NextStartSeconds != nil && r.NextCueIndex != nil {
		fmt.Fprintf(&out, "\n\n[... truncated at %s of %s — continue with start_seconds=%s or start_cue=%d ...]",
			transcript.Timestamp(r.EndSeconds), transcript.Timestamp(float64(durationSeconds)), formatSeconds(*r.NextStartSeconds), *r.NextCueIndex)
	}
	r.Text = out.String()
	return r
}

func formatSeconds(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// SelectForJSON returns the cue-level counterpart of Render.
func SelectForJSON(segs []transcript.Segment, durationSeconds int, opts Options) JSONSelection {
	sel, cappedNext := selectSegments(segs, opts)
	res := JSONSelection{TotalChars: canonicalChars(segs)}
	chars := 0
	for _, s := range sel {
		c := utf8.RuneCountInString(s.seg.Text) + 1
		if opts.MaxChars > 0 && len(res.Segments) > 0 && chars+c > opts.MaxChars {
			res.Truncated = true
			ns := s.seg.Start
			nc := s.i
			res.NextStartSeconds = &ns
			res.NextCueIndex = &nc
			break
		}
		res.Segments = append(res.Segments, s.seg)
		res.ReturnedWords += len(strings.Fields(s.seg.Text))
		chars += c
	}
	res.ReturnedCues = len(res.Segments)
	res.ReturnedChars = chars

	// Same rule as Render: a MaxCues cap inside the requested range is
	// truncation, independent of whether end_seconds was given.
	if !res.Truncated && cappedNext != nil && len(sel) > 0 {
		res.Truncated = true
		ns := segs[*cappedNext].Start
		nc := *cappedNext
		res.NextStartSeconds = &ns
		res.NextCueIndex = &nc
	}

	start := opts.StartSeconds
	end := opts.StartSeconds
	if len(res.Segments) > 0 {
		first := res.Segments[0]
		last := res.Segments[len(res.Segments)-1]
		start = first.Start
		end = last.Start + last.Duration
	}
	res.Coverage = fmt.Sprintf("%s-%s of %s", transcript.Timestamp(start), transcript.Timestamp(end), transcript.Timestamp(float64(durationSeconds)))
	return res
}
