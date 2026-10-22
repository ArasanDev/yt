package render

import (
	"strings"
	"testing"

	"yt/internal/transcript"
)

func testTranscript() *transcript.Transcript {
	var segs []transcript.Segment
	// 40 cues, one per second: enough to span several 30s blocks.
	for i := 0; i < 40; i++ {
		segs = append(segs, transcript.Segment{
			Start: float64(i), Duration: 1, Text: "cue" + string(rune('A'+i%26)) + " word",
		})
	}
	return &transcript.Transcript{
		VideoID:          "testvideo01",
		Source:           "innertube-android",
		Language:         "en",
		OriginalLanguage: "en",
		Generated:        true,
		DurationSeconds:  40,
		Segments:         segs,
	}
}

func TestRenderBlocksAndTimestamps(t *testing.T) {
	tr := testTranscript()
	r := Render(tr, 40, Options{Granularity: "block", TimestampMode: "block", MaxChars: 0})
	if r.TotalCues != 40 {
		t.Fatalf("want 40 total cues, got %d", r.TotalCues)
	}
	if r.ReturnedCues != 40 {
		t.Fatalf("want all cues returned, got %d", r.ReturnedCues)
	}
	if r.Truncated {
		t.Fatal("should not be truncated with unlimited budget")
	}
	if !strings.HasPrefix(r.Text, "# transcript") {
		t.Errorf("missing header: %q", r.Text[:min(40, len(r.Text))])
	}
	// 40s of speech in 30s blocks => two lines.
	body := r.Text[strings.Index(r.Text, "\n\n")+2:]
	lines := nonEmptyLines(body)
	if len(lines) != 2 {
		t.Errorf("want 2 blocks, got %d: %v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "[00:00]") {
		t.Errorf("block timestamp mode should stamp the first line: %q", lines[0])
	}
}

func TestRenderCueGranularity(t *testing.T) {
	tr := testTranscript()
	r := Render(tr, 40, Options{Granularity: "cue", TimestampMode: "cue", MaxChars: 0})
	body := r.Text[strings.Index(r.Text, "\n\n")+2:]
	if got := len(nonEmptyLines(body)); got != 40 {
		t.Errorf("want 40 cue lines, got %d", got)
	}
}

func TestRenderTruncationHeaderAndPointers(t *testing.T) {
	tr := testTranscript()
	r := Render(tr, 40, Options{Granularity: "cue", TimestampMode: "cue", MaxChars: 400})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}
	if r.NextStartSeconds == nil || r.NextCueIndex == nil {
		t.Fatal("truncation must report continuation pointers")
	}
	if !strings.Contains(r.Text, "TRUNCATED") {
		t.Error("truncation must be visible in the header, not only the footer")
	}
	if *r.NextCueIndex <= 0 || r.ReturnedCues >= 40 {
		t.Errorf("unexpected pointers: cue=%d returned=%d", *r.NextCueIndex, r.ReturnedCues)
	}
	// Continuing from the pointer must make progress and eventually finish.
	rest := Render(tr, 40, Options{Granularity: "cue", TimestampMode: "cue", StartCue: *r.NextCueIndex, MaxChars: 0})
	if rest.ReturnedCues == 0 {
		t.Fatal("continuation returned nothing")
	}
}

func TestRenderTimeRange(t *testing.T) {
	tr := testTranscript()
	r := Render(tr, 40, Options{Granularity: "cue", TimestampMode: "cue", StartSeconds: 10, EndSeconds: 20})
	if r.ReturnedCues != 10 {
		t.Fatalf("want 10 cues for 10s-20s window, got %d", r.ReturnedCues)
	}
	if !strings.HasPrefix(r.Coverage, "00:10-") {
		t.Errorf("unexpected coverage: %q", r.Coverage)
	}
}

func TestRenderSentenceFallsBackToBlock(t *testing.T) {
	tr := testTranscript()
	r := Render(tr, 40, Options{Granularity: "sentence"})
	if r.Granularity != "block" {
		t.Errorf("sentence should degrade to block, got %q", r.Granularity)
	}
	if len(r.Notes) == 0 {
		t.Error("degradation must be reported in notes")
	}
}

func TestTimestampModeNone(t *testing.T) {
	tr := testTranscript()
	r := Render(tr, 40, Options{Granularity: "cue", TimestampMode: "none"})
	if strings.Contains(r.Text, "[00:0") {
		t.Error("timestamp_mode=none must not emit stamps")
	}
}

// Regression: the resume pointer used to be rounded to one decimal. Because
// selectSegments skips cues that start before start_seconds, rounding 3.456 up
// to "3.5" silently dropped the boundary cue.
func TestResumePointerIsExact(t *testing.T) {
	tr := &transcript.Transcript{VideoID: "v", DurationSeconds: 10, Segments: []transcript.Segment{
		{Start: 0, Duration: 1, Text: "a"},
		{Start: 3.456, Duration: 1, Text: "boundary cue"},
		{Start: 5, Duration: 1, Text: "c"},
	}}
	r := Render(tr, 10, Options{Granularity: "cue", TimestampMode: "cue", MaxChars: 10})
	if !r.Truncated || r.NextStartSeconds == nil {
		t.Fatalf("expected truncation, got %+v", r)
	}
	if *r.NextStartSeconds != 3.456 {
		t.Fatalf("pointer must be exact, got %v", *r.NextStartSeconds)
	}
	if !strings.Contains(r.Text, "next_start_seconds=3.456") {
		t.Errorf("printed pointer is rounded: %q", r.Text)
	}
	rest := Render(tr, 10, Options{
		Granularity: "cue", TimestampMode: "cue", StartSeconds: *r.NextStartSeconds,
	})
	if !strings.Contains(rest.Text, "boundary cue") {
		t.Errorf("resuming at the pointer dropped the boundary cue: %q", rest.Text)
	}
}

func TestMaxCuesReportsContinuation(t *testing.T) {
	tr := testTranscript()
	r := Render(tr, 40, Options{Granularity: "cue", TimestampMode: "cue", MaxCues: 5})
	if !r.Truncated || r.NextCueIndex == nil || *r.NextCueIndex != 5 {
		t.Fatalf("max_cues must report the next cue, got %+v", r)
	}
	if r.ReturnedCues != 5 {
		t.Errorf("want 5 returned cues, got %d", r.ReturnedCues)
	}
}

func TestRenderEmptyRangeIsFlagged(t *testing.T) {
	tr := testTranscript()
	r := Render(tr, 40, Options{Granularity: "cue", StartSeconds: 100})
	if r.ReturnedCues != 0 {
		t.Fatalf("want empty selection, got %d", r.ReturnedCues)
	}
	if len(r.Notes) == 0 || !strings.Contains(strings.Join(r.Notes, " "), "no cues") {
		t.Errorf("empty range must be reported, notes=%v", r.Notes)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
