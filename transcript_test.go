package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestExtractVideoID(t *testing.T) {
	cases := map[string]string{
		"dQw4w9WgXcQ": "dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":    "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?si=abc":            "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":     "dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ&t=10": "dQw4w9WgXcQ",
	}
	for in, want := range cases {
		got, err := extractVideoID(in)
		if err != nil {
			t.Fatalf("extractVideoID(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("extractVideoID(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := extractVideoID("not a video"); err == nil {
		t.Fatal("expected error for invalid input")
	} else if te, ok := toolErrorOf(err); !ok || te.Code != CodeInvalidID {
		t.Fatalf("want invalid_id, got %v", err)
	}
}

func TestParseJSON3(t *testing.T) {
	body := []byte(`{"events":[
		{"tStartMs":0,"dDurationMs":1500,"segs":[{"utf8":"Hello "},{"utf8":"world"}]},
		{"tStartMs":1500,"dDurationMs":1000,"segs":[{"utf8":"\n"}]},
		{"tStartMs":2000,"dDurationMs":1000,"segs":[{"utf8":"Bye"}]}
	]}`)
	segs, err := parseJSON3(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %d", len(segs))
	}
	if segs[0].Text != "Hello world" {
		t.Errorf("want %q, got %q", "Hello world", segs[0].Text)
	}
}

// Regression: srv3 wraps word runs in <s> tags; stripping tags without a
// separator produced "Helloworld".
func TestParseTimedXMLJoinsRuns(t *testing.T) {
	body := []byte(`<transcript><body>
		<p t="0" d="1000"><s>Hello</s><s>world</s></p>
		<p t="1000" d="1000"><s>again</s><br/><s>now</s></p>
	</body></transcript>`)
	segs, err := parseTimedXML(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %d", len(segs))
	}
	if segs[0].Text != "Hello world" {
		t.Errorf("run joining broken: got %q", segs[0].Text)
	}
	if segs[1].Text != "again now" {
		t.Errorf("<br> handling broken: got %q", segs[1].Text)
	}
}

func TestParseTimedXMLDurationFallback(t *testing.T) {
	body := []byte(`<transcript><body>
		<p t="0"><s>a</s></p>
		<p t="2500"><s>b</s></p>
	</body></transcript>`)
	segs, err := parseTimedXML(body)
	if err != nil {
		t.Fatal(err)
	}
	if segs[0].Duration != 2.5 {
		t.Errorf("want derived duration 2.5, got %v", segs[0].Duration)
	}
}

func TestPickTrack(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "es", Kind: "asr", IsTranslatable: true},
		{LanguageCode: "en", Kind: "asr", IsTranslatable: true},
	}

	got, translated, _, err := pickTrack(tracks, []string{"en"}, true)
	if err != nil || translated || got.LanguageCode != "en" {
		t.Fatalf("exact match failed: %+v translated=%v err=%v", got, translated, err)
	}

	got, translated, target, err := pickTrack(tracks, []string{"fr"}, true)
	if err != nil || !translated || target != "fr" {
		t.Fatalf("translation fallback failed: %+v translated=%v target=%q err=%v", got, translated, target, err)
	}

	_, _, _, err = pickTrack(tracks, []string{"fr"}, false)
	te, ok := toolErrorOf(err)
	if !ok || te.Code != CodeNoMatchingLang {
		t.Fatalf("want no_matching_language, got %v", err)
	}
	if len(te.AvailableLanguages) != 2 {
		t.Errorf("want available languages listed, got %v", te.AvailableLanguages)
	}

	got, translated, _, err = pickTrack(tracks, []string{OriginalLanguageValue}, true)
	if err != nil || translated || got.LanguageCode != "es" {
		t.Fatalf("original pick failed: %+v translated=%v err=%v", got, translated, err)
	}
}

// Regression: caption base URLs already carry fmt=srv3, so appending
// &fmt=json3 was ignored by YouTube and every transcript paid for a wasted
// XML request before falling back.
func TestOriginalTrackUsesDefaultAudioTrack(t *testing.T) {
	pr := &playerResponse{}
	pr.Captions.Renderer.CaptionTracks = []captionTrack{
		{LanguageCode: "ar", Kind: "asr"},
		{LanguageCode: "bn", Kind: "asr"},
		{LanguageCode: "en", Kind: "asr"},
	}
	pr.Captions.Renderer.AudioTracks = []audioTrack{
		{AudioTrackID: "ar.10", CaptionTrackIndices: []int{0, 2}},
		{AudioTrackID: "en-US.4", CaptionTrackIndices: []int{2, 0}},
	}
	pr.Captions.Renderer.DefaultAudioTrackIndex = 1
	if got := originalTrackFrom(pr); got == nil || got.LanguageCode != "en" {
		t.Fatalf("default audio track should resolve en, got %+v", got)
	}

	pr.Captions.Renderer.DefaultAudioTrackIndex = 0
	if got := originalTrackFrom(pr); got == nil || got.LanguageCode != "ar" {
		t.Fatalf("default audio track should resolve ar, got %+v", got)
	}
}

func TestCaptionURLReplacesFormatAndEscapesTarget(t *testing.T) {
	u, err := captionURL("https://example.com/t?fmt=srv3&x=1&signature=abc", "json3", "en&bad=1", true)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("fmt") != "json3" {
		t.Errorf("fmt must be replaced, got %q", q.Get("fmt"))
	}
	if q.Get("tlang") != "en&bad=1" {
		t.Errorf("tlang must not inject params, got %q", q.Get("tlang"))
	}
	if q.Get("bad") != "" {
		t.Error("tlang injection leaked an extra query parameter")
	}
	if q.Get("signature") != "abc" {
		t.Error("signature must be preserved")
	}
}

func TestPickTrackHonoursSentinelPosition(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "es", Kind: "", IsTranslatable: true},
		{LanguageCode: "en", Kind: "asr", IsTranslatable: true},
	}
	got, translated, _, err := pickTrack(tracks, []string{"en", OriginalLanguageValue}, true)
	if err != nil || translated || got.LanguageCode != "en" {
		t.Fatalf("first requested language must win: %+v translated=%v err=%v", got, translated, err)
	}
}

func TestTruncateRunes(t *testing.T) {
	s := "héllo wörld"
	got, truncated := truncateRunes(s, 5)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.HasPrefix(got, "héllo") || !strings.HasSuffix(got, "…") {
		t.Errorf("rune-unsafe truncation: %q", got)
	}
	if full, tr := truncateRunes(s, 0); tr || full != s {
		t.Errorf("limit 0 should mean no truncation, got %q truncated=%v", full, tr)
	}
	if _, tr := truncateRunes(s, 100); tr {
		t.Error("short string should not be truncated")
	}
}

// Auto-dubbed videos list one ASR track per language. Requesting Spanish on an
// English video used to return translated=false with original_language="es",
// which presents machine-translated speech as the speaker's own words.
func TestAutoDubbedASRTrackIsMarkedTranslated(t *testing.T) {
	en := &captionTrack{LanguageCode: "en", Kind: "asr"}
	es := &captionTrack{LanguageCode: "es", Kind: "asr"}

	lang, orig, target, translated := transcriptIdentity(es, en, false, "")
	if lang != "es" || orig != "en" || !translated || target != "es" {
		t.Fatalf("auto-dub identity = lang=%q orig=%q target=%q translated=%v", lang, orig, target, translated)
	}

	lang, orig, target, translated = transcriptIdentity(en, en, false, "")
	if lang != "en" || orig != "en" || translated || target != "" {
		t.Fatalf("original identity = lang=%q orig=%q target=%q translated=%v", lang, orig, target, translated)
	}

	// On-the-fly translation: language is the target, original is the source.
	lang, orig, target, translated = transcriptIdentity(en, en, true, "fr")
	if lang != "fr" || orig != "en" || !translated || target != "fr" {
		t.Fatalf("tlang identity = lang=%q orig=%q target=%q translated=%v", lang, orig, target, translated)
	}

	// Without audio-track metadata we must not guess: keep the legacy fields.
	lang, orig, target, translated = transcriptIdentity(es, nil, false, "")
	if lang != "es" || orig != "es" || translated || target != "" {
		t.Fatalf("unknown-original identity = lang=%q orig=%q target=%q translated=%v", lang, orig, target, translated)
	}
}

// The translation fallback must translate the uploader's track, not the first
// ASR track in the list (which can itself be an auto-dub).
func TestTranslationFallbackUsesOriginalTrack(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "ar", Kind: "asr", IsTranslatable: true},
		{LanguageCode: "en", Kind: "asr", IsTranslatable: true},
		{LanguageCode: "es", Kind: "asr", IsTranslatable: true},
	}
	original := &tracks[1]
	got, translated, target, err := pickTrackWithOriginal(tracks, original, []string{"sv"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !translated || target != "sv" || got.LanguageCode != "en" {
		t.Fatalf("translation source = %q translated=%v target=%q, want en->sv", got.LanguageCode, translated, target)
	}
}

func TestOriginalTrackFromNeedsAudioMetadata(t *testing.T) {
	pr := &playerResponse{}
	pr.Captions.Renderer.CaptionTracks = []captionTrack{
		{LanguageCode: "ar", Kind: "asr"},
		{LanguageCode: "en", Kind: "asr"},
	}
	if got := originalTrackFrom(pr); got != nil {
		t.Fatalf("without audio tracks the original must be unknown, got %+v", got)
	}
}
