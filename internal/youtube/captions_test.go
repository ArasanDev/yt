package youtube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ArasanDev/yt/internal/apperr"
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
		got, err := ExtractVideoID(in)
		if err != nil {
			t.Fatalf("ExtractVideoID(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ExtractVideoID(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ExtractVideoID("not a video"); err == nil {
		t.Fatal("expected error for invalid input")
	} else if te, ok := apperr.As(err); !ok || te.Code != apperr.CodeInvalidID {
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
	te, ok := apperr.As(err)
	if !ok || te.Code != apperr.CodeNoMatchingLang {
		t.Fatalf("want no_matching_language, got %v", err)
	}
	if len(te.AvailableLanguages) != 2 {
		t.Errorf("want available languages listed, got %v", te.AvailableLanguages)
	}

	got, translated, _, err = pickTrack(tracks, []string{OriginalLanguageValue}, true)
	te, ok = apperr.As(err)
	if !ok || te.Code != apperr.CodeNoMatchingLang || got != nil {
		t.Fatalf("original sentinel with unknown original must fail loudly, got %+v err=%v", got, err)
	}
	if len(te.AvailableLanguages) != 2 {
		t.Errorf("unknown-original error should list available languages, got %v", te.AvailableLanguages)
	}
}

func TestPickTrackNoTranslatableTrack(t *testing.T) {
	tracks := []captionTrack{{LanguageCode: "en", Kind: "asr", IsTranslatable: false}}
	_, _, _, err := pickTrack(tracks, []string{"fr"}, true)
	te, ok := apperr.As(err)
	if !ok || te.Code != apperr.CodeNoMatchingLang {
		t.Fatalf("want no_matching_language, got %v", err)
	}
	if strings.Contains(te.Suggestion, "allow_translation=true") {
		t.Errorf("suggestion tells caller to enable a flag that is already on: %q", te.Suggestion)
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

	// Without audio-track metadata the original is unknown: OriginalLanguage
	// stays empty (explicit "unknown") instead of claiming the chosen track is
	// the original.
	lang, orig, target, translated = transcriptIdentity(es, nil, false, "")
	if lang != "es" || orig != "" || translated || target != "" {
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

// YouTube's audio→caption mapping is authoritative: when CaptionTrackIndices
// disagrees with a language match, the mapping must win.
func TestOriginalTrackPrefersCaptionTrackIndices(t *testing.T) {
	pr := &playerResponse{}
	pr.Captions.Renderer.CaptionTracks = []captionTrack{
		{LanguageCode: "en", Kind: "asr"},
		{LanguageCode: "es", Kind: "asr"},
	}
	pr.Captions.Renderer.AudioTracks = []audioTrack{
		{AudioTrackID: "es.10", CaptionTrackIndices: []int{0}},
	}
	pr.Captions.Renderer.DefaultAudioTrackIndex = 0
	if got := originalTrackFrom(pr); got == nil || got.LanguageCode != "en" {
		t.Fatalf("captionTrackIndices must beat language matching, got %+v", got)
	}
}

// A single-language response is the only unambiguous case without audio
// metadata; the manual track is the original.
func TestInferredOriginalSingleLanguageOnly(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "en", Kind: "asr"},
		{LanguageCode: "en-GB"},
	}
	got := inferredOriginal(tracks)
	if got == nil || got.LanguageCode != "en-GB" {
		t.Fatalf("single base language should infer the manual track, got %+v", got)
	}
	if got := inferredOriginal(tracks[:1]); got == nil || got.LanguageCode != "en" {
		t.Fatalf("a single track is its own original, got %+v", got)
	}
	if got := inferredOriginal([]captionTrack{
		{LanguageCode: "ar", Kind: "asr"},
		{LanguageCode: "en", Kind: "asr"},
	}); got != nil {
		t.Fatalf("multiple ASR languages give no original, got %+v", got)
	}
}

// A manual track in another language (an uploader-supplied translation) is a
// translation just like an ASR dub; generated/ASR is reported separately.
func TestManualDubOnEnglishOriginalIsTranslated(t *testing.T) {
	en := &captionTrack{LanguageCode: "en"}
	manualEs := &captionTrack{LanguageCode: "es"} // Kind "" = manual
	lang, orig, target, translated := transcriptIdentity(manualEs, en, false, "")
	if lang != "es" || orig != "en" || !translated || target != "es" {
		t.Fatalf("manual dub identity = lang=%q orig=%q target=%q translated=%v", lang, orig, target, translated)
	}
	if manualEs.Kind == "asr" {
		t.Fatal("test setup: the Spanish track must be manual")
	}
	// Same language, ASR kind: not translated, only generated.
	enASR := &captionTrack{LanguageCode: "en", Kind: "asr"}
	lang, orig, target, translated = transcriptIdentity(enASR, en, false, "")
	if lang != "en" || orig != "en" || translated || target != "" {
		t.Fatalf("ASR in original language = lang=%q orig=%q target=%q translated=%v", lang, orig, target, translated)
	}
}

// Language selection must prefer an exact code match over a regional variant,
// an unqualified base over a different region, and stay deterministic.
func TestPickLangTrackPrefersExactAndUnqualified(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "pt-BR"},
		{LanguageCode: "pt"},
		{LanguageCode: "en-GB"},
	}
	if got := pickLangTrack(tracks, "pt", false); got == nil || got.LanguageCode != "pt" {
		t.Fatalf("exact match must beat regional variant, got %+v", got)
	}
	if got := pickLangTrack(tracks, "en", false); got == nil || got.LanguageCode != "en-GB" {
		t.Fatalf("base-language match should be deterministic, got %+v", got)
	}
	mixed := []captionTrack{{LanguageCode: "en-GB"}, {LanguageCode: "en"}}
	if got := pickLangTrack(mixed, "en-us", false); got == nil || got.LanguageCode != "en" {
		t.Fatalf("unqualified base should beat another region, got %+v", got)
	}
	if got := pickLangTrack(tracks, "fr", false); got != nil {
		t.Fatalf("no match expected for fr, got %+v", got)
	}
}

// Regression: out-of-order events, exact duplicate cues, and missing or
// negative tStartMs must not poison cue order for range selection, block
// boundaries, or SRT output.
func TestParseJSON3SortsDedupesAndHandlesMissingStart(t *testing.T) {
	body := []byte(`{"events":[
		{"tStartMs":2000,"dDurationMs":1000,"segs":[{"utf8":"second"}]},
		{"dDurationMs":1000,"segs":[{"utf8":"missing start"}]},
		{"tStartMs":0,"dDurationMs":1000,"segs":[{"utf8":"first"}]},
		{"tStartMs":2000,"dDurationMs":1000,"segs":[{"utf8":"second"}]},
		{"tStartMs":-5,"dDurationMs":500,"segs":[{"utf8":"negative start"}]},
		{"tStartMs":3000,"dDurationMs":1000,"segs":[{"utf8":"third"}]}
	]}`)
	segs, err := parseJSON3(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		start float64
		text  string
	}{
		{0, "first"},
		{2, "second"},
		{2, "missing start"},
		{2, "negative start"},
		{3, "third"},
	}
	if len(segs) != len(want) {
		t.Fatalf("want %d segments (duplicate removed), got %d: %+v", len(want), len(segs), segs)
	}
	dups := 0
	for i, w := range want {
		if segs[i].Start != w.start || segs[i].Text != w.text {
			t.Errorf("segment %d = %v %q, want %v %q", i, segs[i].Start, segs[i].Text, w.start, w.text)
		}
		if i > 0 && segs[i].Start < segs[i-1].Start {
			t.Errorf("starts must be non-decreasing at %d: %+v", i, segs)
		}
		if segs[i].Text == "second" {
			dups++
		}
	}
	if dups != 1 {
		t.Errorf("exact duplicate cue must be removed, kept %d copies", dups)
	}
}

func captionTestClient(t *testing.T, handler http.HandlerFunc) (*Client, string, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	return NewClientWithConfig(Config{HTTPClient: srv.Client()}), srv.URL + "/captions", srv.Close
}

func TestCaptionDownload403IsBlocked(t *testing.T) {
	c, base, cleanup := captionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	defer cleanup()
	_, err := c.downloadSegments(context.Background(), base, "", false)
	te, ok := apperr.As(err)
	if !ok || te.Code != apperr.CodeBlocked {
		t.Fatalf("403 must classify as blocked, got %v", err)
	}
}

func TestCaptionDownloadHTMLBotCheckIsBotCheck(t *testing.T) {
	page := `<!DOCTYPE html><html><head><title>https://www.youtube.com</title></head>` +
		`<body>Our systems have detected unusual traffic from your computer network.</body></html>`
	c, base, cleanup := captionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})
	defer cleanup()
	_, err := c.downloadSegments(context.Background(), base, "", false)
	te, ok := apperr.As(err)
	if !ok || te.Code != apperr.CodeBotCheck {
		t.Fatalf("200 bot-check HTML must classify as bot_check, got %v", err)
	}
}

func TestCaptionDownloadHTMLIsUpstreamChanged(t *testing.T) {
	page := `<!DOCTYPE html><html><body><h1>Something broke on our side</h1></body></html>`
	c, base, cleanup := captionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})
	defer cleanup()
	_, err := c.downloadSegments(context.Background(), base, "", false)
	te, ok := apperr.As(err)
	if !ok || te.Code != apperr.CodeUpstreamChanged {
		t.Fatalf("200 non-caption HTML must classify as upstream_changed, got %v", err)
	}
}

// The json3 -> srv3 fallback must survive an HTML response on json3.
func TestCaptionDownloadFallsBackWhenJSON3ReturnsHTML(t *testing.T) {
	c, base, cleanup := captionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fmt") == "srv3" {
			w.Write([]byte(`<transcript><body><p t="0" d="1000"><s>Hello</s><s>world</s></p></body></transcript>`))
			return
		}
		w.Write([]byte(`<!DOCTYPE html><html><body>unexpected page</body></html>`))
	})
	defer cleanup()
	segs, err := c.downloadSegments(context.Background(), base, "", false)
	if err != nil {
		t.Fatalf("srv3 fallback after HTML json3 failed: %v", err)
	}
	if len(segs) != 1 || segs[0].Text != "Hello world" {
		t.Fatalf("unexpected segments: %+v", segs)
	}
}
