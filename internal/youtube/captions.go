// Track selection and caption download/parsing.
package youtube

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/ArasanDev/yt/internal/apperr"
	"github.com/ArasanDev/yt/internal/transcript"
)

// TranscriptOptions controls caption-track selection.
type TranscriptOptions struct {
	Languages        []string
	AllowTranslation bool
}

func normalizeLanguages(langs []string) []string {
	var out []string
	for _, l := range langs {
		l = strings.TrimSpace(strings.ToLower(l))
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// selectAndDownload picks a track and downloads it. Translation is opt-in:
// when a requested language is unavailable and translation is disabled, the
// caller gets a typed error listing what is available.
func (c *Client) selectAndDownload(ctx context.Context, videoID string, pr *playerResponse, via string, opts TranscriptOptions) (*transcript.Transcript, error) {
	tracks := pr.tracks()
	if len(tracks) == 0 {
		te := apperr.New(apperr.CodeNoCaptions, "This video has no caption tracks.")
		te.Suggestion = "Captions may be disabled by the uploader; get_video_info confirms availability."
		return nil, te
	}
	orig := originalTrackFrom(pr)
	track, needsTlang, target, err := pickTrackWithOriginal(tracks, orig, opts.Languages, opts.AllowTranslation)
	if err != nil {
		return nil, err
	}
	segs, err := c.downloadSegments(ctx, track.BaseURL, target, needsTlang)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		te := apperr.New(apperr.CodeNoCaptions, "The caption track for %s is empty.", track.LanguageCode)
		te.Suggestion = "Retry or request a different language; the track may be unusable."
		return nil, te
	}
	language, originalLanguage, translationTarget, translated := transcriptIdentity(track, orig, needsTlang, target)
	return &transcript.Transcript{
		VideoID:            videoID,
		Source:             via,
		Language:           language,
		OriginalLanguage:   originalLanguage,
		TranslationTarget:  translationTarget,
		RequestedLanguages: append([]string(nil), opts.Languages...),
		Generated:          track.Kind == "asr",
		Translated:         translated,
		DurationSeconds:    pr.durationSeconds(),
		Segments:           segs,
	}, nil
}

// transcriptIdentity describes what the selected track actually contains.
// needsTlang means YouTube is translating the fetched track on the fly.
//
// Auto-dubbed videos expose one ASR track per audio language, and every
// non-original track is a translation served as a track. Requesting "es" on an
// English video must not report translated=false with original_language="es":
// that presents translated speech as the speaker's words. A manual track in
// another language (an uploader-supplied translation) is just as much a
// translation as an ASR dub; generated/ASR is reported separately.
//
// When the player response carries no audio-track metadata the original is
// unknown. OriginalLanguage is then left empty — an explicit "unknown" —
// instead of being filled with the chosen track's language. This is a
// deliberate representation choice over a hard error: responses without audio
// metadata are routine (ANDROID/IOS clients, the watch-page fallback), and
// erroring would fail legitimate fetches where the caller asked for a specific
// language and never claimed provenance.
func transcriptIdentity(track, original *captionTrack, needsTlang bool, target string) (language, originalLanguage, translationTarget string, translated bool) {
	language = track.LanguageCode
	if original != nil {
		originalLanguage = original.LanguageCode
	}
	switch {
	case needsTlang:
		language = target
		translationTarget = target
		translated = true
	case original != nil && !langMatches(track.LanguageCode, original.LanguageCode):
		translated = true
		translationTarget = track.LanguageCode
	}
	return
}

// pickTrack prefers, for each requested language in order: a manual track, then
// an auto-generated track. The sentinel "original" is honoured at its position
// in the list, so "es,original" tries Spanish first and only falls back to the
// uploader's own language.
//
// If allowTranslation is false and nothing matches, it returns a typed
// no_matching_language error rather than silently translating.
func pickTrack(tracks []captionTrack, langs []string, allowTranslation bool) (*captionTrack, bool, string, error) {
	return pickTrackWithOriginal(tracks, nil, langs, allowTranslation)
}

// pickTrackWithOriginal is pickTrack plus the actual uploader-language track
// (resolved from the player response). Passing nil means the original is
// unknown: the "original" sentinel then resolves only when the response lists
// a single caption language, and fails loudly instead of silently serving
// tracks[0] as "the original" when multiple languages exist.
func pickTrackWithOriginal(tracks []captionTrack, original *captionTrack, langs []string, allowTranslation bool) (*captionTrack, bool, string, error) {
	langs = normalizeLanguages(langs)
	sawOriginal, sawOther := false, false
	for _, lang := range langs {
		if lang == OriginalLanguageValue {
			sawOriginal = true
			src := original
			if src == nil {
				src = inferredOriginal(tracks)
			}
			if src != nil {
				return src, false, "", nil
			}
			continue
		}
		sawOther = true
		for _, wantASR := range []bool{false, true} {
			if t := pickLangTrack(tracks, lang, wantASR); t != nil {
				return t, false, "", nil
			}
		}
	}
	// Only "original" was asked for and the response gives no way to know it:
	// fail with the reason instead of guessing (or, with translation enabled,
	// machine-translating from an arbitrary source track).
	if sawOriginal && !sawOther && original == nil && inferredOriginal(tracks) == nil {
		te := apperr.New(apperr.CodeNoMatchingLang,
			"The original language cannot be determined: the response lists multiple caption languages without audio-track metadata.")
		te.AvailableLanguages = trackLanguages(tracks)
		te.Suggestion = "Request a specific language from available_languages instead of \"original\"."
		return nil, false, "", te
	}
	if !allowTranslation {
		te := noMatchingLanguage(tracks, langs)
		if sawOriginal && original == nil {
			te.Suggestion = "The original language could not be determined from the player response; request a specific language from available_languages."
		}
		return nil, false, "", te
	}
	// Translate from the uploader's own track, not whichever track YouTube
	// happened to list first: auto-dubbed videos list one ASR track per
	// language, so the first entry can be a translation of a translation.
	src := original
	if src == nil || !src.IsTranslatable {
		src = nil
		for _, wantASR := range []bool{false, true} {
			for i := range tracks {
				t := &tracks[i]
				if (t.Kind == "asr") == wantASR && t.IsTranslatable {
					src = t
					break
				}
			}
			if src != nil {
				break
			}
		}
	}
	if src != nil {
		target := "en"
		if len(langs) > 0 && langs[0] != OriginalLanguageValue {
			target = langs[0]
		}
		return src, true, target, nil
	}
	te := noMatchingLanguage(tracks, langs)
	if allowTranslation {
		te.Message = "No caption track matches the requested languages, and no remaining track is translatable."
		te.Suggestion = "Retry with one of available_languages or pass \"original\"."
	}
	return nil, false, "", te
}

// pickLangTrack selects the best manual (or, with wantASR, auto-generated)
// track for a requested language. Exact code matches win over base-language
// matches, and an unqualified candidate ("en" for an "en-us" request) beats a
// different regional variant ("en-gb"), so the same response always yields
// the same track instead of whichever variant the list happened to serve
// first. Ties keep list order, which is deterministic.
func pickLangTrack(tracks []captionTrack, lang string, wantASR bool) *captionTrack {
	var best *captionTrack
	bestRank := 0
	for i := range tracks {
		t := &tracks[i]
		if (t.Kind == "asr") != wantASR {
			continue
		}
		r := langRank(t.LanguageCode, lang)
		if r < 0 {
			continue
		}
		if best == nil || r < bestRank {
			best, bestRank = t, r
		}
	}
	return best
}

// langRank scores how well a track code satisfies a request: 0 = exact,
// 1 = request base with an unqualified candidate, 2 = same base language,
// -1 = no match.
func langRank(code, want string) int {
	switch {
	case code == want:
		return 0
	case baseLang(want) != want && code == baseLang(want):
		return 1
	case langMatches(code, want):
		return 2
	default:
		return -1
	}
}

// inferredOriginal recovers the original track from responses that carry no
// audio-track metadata. It only answers when the evidence is unambiguous: a
// single caption language, where the manual track (if any) is preferred. A
// multi-language list (auto-dubbing emits one ASR track per audio language)
// carries no signal about which language the speaker used; guessing wrong
// would brand a machine dub as the speaker's own words, so it returns nil and
// provenance stays unknown.
func inferredOriginal(tracks []captionTrack) *captionTrack {
	if len(tracks) == 0 {
		return nil
	}
	lang := baseLang(tracks[0].LanguageCode)
	for i := range tracks {
		if baseLang(tracks[i].LanguageCode) != lang {
			return nil
		}
	}
	for i := range tracks {
		if tracks[i].Kind != "asr" {
			return &tracks[i]
		}
	}
	return &tracks[0]
}

func baseLang(code string) string {
	if i := strings.IndexByte(code, '-'); i > 0 {
		return code[:i]
	}
	return code
}

func langMatches(code, lang string) bool {
	return baseLang(code) == baseLang(lang)
}

// originalTrackFrom identifies the uploader's own language instead of assuming
// the first track is it. Videos with auto-dubbing expose one ASR track per
// language, and the first entry is arbitrary (e.g. Arabic for an English
// video).
//
// Resolution order:
//  1. The default audio track's CaptionTrackIndices mapping — YouTube's own
//     authoritative audio→caption association, preferred over any matching.
//  2. A language match against the default audio track's id (manual first).
//  3. inferredOriginal, which answers only single-language responses.
//
// It returns nil when the original cannot be established. Callers that report
// provenance must not guess: nil means "unknown", not "first track".
func originalTrackFrom(pr *playerResponse) *captionTrack {
	tracks := pr.tracks()
	if len(tracks) == 0 {
		return nil
	}
	r := pr.Captions.Renderer
	idx := r.DefaultAudioTrackIndex
	if len(r.AudioTracks) > 0 && idx >= 0 && idx < len(r.AudioTracks) {
		at := r.AudioTracks[idx]
		// YouTube's own mapping is authoritative: honour it before falling
		// back to language matching, which can pick a same-language dub.
		for _, ci := range at.CaptionTrackIndices {
			if ci >= 0 && ci < len(tracks) {
				return &tracks[ci]
			}
		}
		if lang := audioTrackBaseLang(at.AudioTrackID); lang != "" {
			if t := pickLangTrack(tracks, lang, false); t != nil {
				return t
			}
			if t := pickLangTrack(tracks, lang, true); t != nil {
				return t
			}
		}
	}
	return inferredOriginal(tracks)
}

// audioTrackBaseLang strips the trailing channel suffix from an audio track id
// like "en-US.4".
func audioTrackBaseLang(id string) string {
	if i := strings.IndexByte(id, '.'); i > 0 {
		return id[:i]
	}
	return id
}

func noMatchingLanguage(tracks []captionTrack, langs []string) *apperr.Error {
	te := apperr.New(apperr.CodeNoMatchingLang,
		"No caption track matches requested languages %s.", strings.Join(langs, ","))
	te.AvailableLanguages = trackLanguages(tracks)
	te.Suggestion = "Retry with one of available_languages, pass \"original\", or set allow_translation=true."
	return te
}

func trackLanguages(tracks []captionTrack) []string {
	var out []string
	for _, t := range tracks {
		out = append(out, t.LanguageCode)
	}
	return uniqueStrings(out)
}

// captionURL sets (replacing, not appending) the format on the signed caption
// URL. YouTube's base URLs already carry fmt=srv3, and appending &fmt=json3 is
// ignored because the first fmt wins — which silently made json3 unreachable
// and cost a wasted request per transcript.
func captionURL(baseURL, format, tlang string, translate bool) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", apperr.New(apperr.CodeUpstreamChanged, "invalid caption URL: %v", err)
	}
	q := u.Query()
	q.Set("fmt", format)
	if translate && tlang != "" {
		q.Set("tlang", tlang)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// downloadSegments requests json3 first, then the legacy srv3 XML, replacing
// the format parameter. A transport failure on one format no longer aborts the
// fallback, and if both formats parse to zero cues the track is genuinely empty
// (reported as no_captions by the caller).
//
// Response classification: a 403 surfaces as the typed blocked error (signed
// caption URLs are short-lived), a 200 bot-check HTML page as bot_check, and
// any other HTML/non-caption payload as upstream_changed — never as an empty
// track.
func (c *Client) downloadSegments(ctx context.Context, baseURL, targetLang string, translate bool) ([]transcript.Segment, error) {
	var errs []error
	for _, format := range []string{"json3", "srv3"} {
		u, err := captionURL(baseURL, format, targetLang, translate)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		resp, err := c.requestWithRetry(ctx, http.MethodGet, u, nil, map[string]string{
			"User-Agent": browserUA,
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		body, err := readBody(resp, 32<<20)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if looksLikeHTML(body) {
			// A 200 HTML page is not a caption payload: distinguish an
			// IP-level bot check from a changed endpoint.
			errs = append(errs, captionBodyError(format, body))
			continue
		}
		var segs []transcript.Segment
		if format == "json3" {
			segs, err = parseJSON3(body)
		} else {
			segs, err = parseTimedXML(body)
		}
		if err != nil {
			errs = append(errs, apperr.New(apperr.CodeUpstreamChanged, "caption %s parse failed: %v", format, err))
			continue
		}
		if len(segs) > 0 {
			return segs, nil
		}
	}
	if len(errs) == 0 {
		return nil, nil
	}
	return nil, apperr.Aggregate(errs)
}

// looksLikeHTML sniffs the leading bytes of a response body.
func looksLikeHTML(body []byte) bool {
	n := len(body)
	if n > 1024 {
		n = 1024
	}
	prefix := strings.ToLower(string(body[:n]))
	return strings.Contains(prefix, "<html") || strings.Contains(prefix, "<!doctype")
}

// captionBodyError classifies a 200 response whose body is HTML instead of a
// caption payload: a bot-check page is an IP-level block, any other HTML page
// means the caption endpoint changed shape.
func captionBodyError(format string, body []byte) *apperr.Error {
	n := len(body)
	if n > 8192 {
		n = 8192
	}
	prefix := strings.ToLower(string(body[:n]))
	for _, marker := range []string{
		"unusual traffic", "not a bot", "confirm you", "sign in to confirm",
		"captcha", "bot check",
	} {
		if strings.Contains(prefix, marker) {
			te := apperr.New(apperr.CodeBotCheck,
				"YouTube served a bot-check page instead of the %s caption track.", format)
			te.Suggestion = "This is an IP-level block, not a problem with the video. Retry later or from another network."
			return te
		}
	}
	te := apperr.New(apperr.CodeUpstreamChanged,
		"YouTube returned an HTML page instead of the %s caption payload.", format)
	te.Suggestion = "Retry once; if it persists, run `yt doctor` and update the client versions."
	return te
}

// ---- json3 format ----

type json3Doc struct {
	Events []struct {
		// tStartMs is a pointer because some payloads omit it; a missing
		// start must not silently collapse to 0 and poison cue ordering,
		// range selection, and block boundaries downstream.
		StartMs    *int64 `json:"tStartMs"`
		DurationMs int64  `json:"dDurationMs"`
		Segs       []struct {
			UTF8 string `json:"utf8"`
		} `json:"segs"`
	} `json:"events"`
}

func parseJSON3(body []byte) ([]transcript.Segment, error) {
	var doc json3Doc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var out []transcript.Segment
	prevStart := int64(0)
	for _, ev := range doc.Events {
		var b strings.Builder
		for _, s := range ev.Segs {
			b.WriteString(s.UTF8)
		}
		text := strings.Join(strings.Fields(b.String()), " ")
		if text == "" {
			continue
		}
		// A cue without a usable start inherits the previous cue's start
		// instead of jumping the timeline back to zero.
		start := prevStart
		if ev.StartMs != nil && *ev.StartMs >= 0 {
			start = *ev.StartMs
		}
		prevStart = start
		out = append(out, transcript.Segment{
			Start:    float64(start) / 1000,
			Duration: float64(ev.DurationMs) / 1000,
			Text:     text,
		})
	}
	// Events are not guaranteed to arrive in order, and repeated events
	// duplicate cues; both would poison SRT output and windowed search.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	out = dedupeExactCues(out)
	deriveDurations(out)
	return out, nil
}

// dedupeExactCues drops cues whose (start, text) pair is identical to an
// earlier cue. Cues sharing a start but differing in text are kept.
func dedupeExactCues(segs []transcript.Segment) []transcript.Segment {
	type cue struct {
		start float64
		text  string
	}
	seen := make(map[cue]struct{}, len(segs))
	out := segs[:0]
	for _, s := range segs {
		k := cue{s.Start, s.Text}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}
	return out
}

// deriveDurations backfills missing durations from the next cue's start so that
// block grouping and SRT output stay sane for both json3 and srv3 payloads.
func deriveDurations(segs []transcript.Segment) {
	for i := range segs {
		if segs[i].Duration <= 0 {
			if i+1 < len(segs) {
				segs[i].Duration = segs[i+1].Start - segs[i].Start
			} else {
				segs[i].Duration = 2
			}
		}
		if segs[i].Duration < 0 {
			segs[i].Duration = 0
		}
	}
}

// ---- legacy timedtext XML ----
//
// srv3 cues wrap word-level runs: <p t="0" d="1000"><s>Hello</s><s>world</s></p>.
// Stripping tags without inserting a separator produced "Helloworld", so the
// <s> boundaries are turned into spaces before the remaining tags are removed.

type timedXML struct {
	Paras []struct {
		T    int64  `xml:"t,attr"`
		D    int64  `xml:"d,attr"`
		HTML string `xml:",innerxml"`
	} `xml:"body>p"`
}

var (
	tagStripper = regexp.MustCompile(`<[^>]+>`)
	openS       = regexp.MustCompile(`<s[^>]*>`)
	closeS      = regexp.MustCompile(`</s>`)
	brTag       = regexp.MustCompile(`<br\s*/?>`)
)

func parseTimedXML(body []byte) ([]transcript.Segment, error) {
	var doc timedXML
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var out []transcript.Segment
	for _, p := range doc.Paras {
		text := p.HTML
		text = brTag.ReplaceAllString(text, " ")
		text = closeS.ReplaceAllString(text, " ")
		text = openS.ReplaceAllString(text, "")
		text = html.UnescapeString(tagStripper.ReplaceAllString(text, ""))
		text = strings.Join(strings.Fields(text), " ")
		if text == "" {
			continue
		}
		out = append(out, transcript.Segment{
			Start:    float64(p.T) / 1000,
			Duration: float64(p.D) / 1000,
			Text:     text,
		})
	}
	// srv3 often omits per-paragraph durations; derive them from the next start.
	deriveDurations(out)
	return out, nil
}
