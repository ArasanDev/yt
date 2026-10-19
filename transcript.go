// Transcript model, track selection, and caption download/parsing.
package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var (
	videoIDPattern = regexp.MustCompile(
		`(?:youtube\.com/(?:watch\?[^#\s]*v=|shorts/|live/|embed/|v/)|youtu\.be/)([\w-]{11})`)
	bareIDPattern = regexp.MustCompile(`^[\w-]{11}$`)
)

// OriginalLanguageValue is the sentinel an agent passes to ask for the video's
// own track with no machine translation.
const OriginalLanguageValue = "original"

func extractVideoID(urlOrID string) (string, error) {
	s := strings.TrimSpace(urlOrID)
	if bareIDPattern.MatchString(s) {
		return s, nil
	}
	if m := videoIDPattern.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	te := newToolError(CodeInvalidID, "could not extract a YouTube video id from %q", urlOrID)
	te.Suggestion = "Pass a watch/youtu.be/shorts/live/embed URL or a bare 11-character video ID."
	return "", te
}

type Segment struct {
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
	Text     string  `json:"text"`
}

type Transcript struct {
	VideoID            string    `json:"video_id"`
	Source             string    `json:"source"` // innertube-android | innertube-ios | watch-page
	Language           string    `json:"language"`
	OriginalLanguage   string    `json:"original_language"`
	TranslationTarget  string    `json:"translation_target,omitempty"`
	RequestedLanguages []string  `json:"requested_languages,omitempty"`
	Generated          bool      `json:"generated"`
	Translated         bool      `json:"translated"`
	DurationSeconds    int       `json:"duration_seconds"`
	Segments           []Segment `json:"segments"`
}

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
func selectAndDownload(ctx context.Context, videoID string, pr *playerResponse, via string, opts TranscriptOptions) (*Transcript, error) {
	tracks := pr.tracks()
	if len(tracks) == 0 {
		te := newToolError(CodeNoCaptions, "This video has no caption tracks.")
		te.Suggestion = "Captions may be disabled by the uploader; get_video_info confirms availability."
		return nil, te
	}
	orig := originalTrackFrom(pr)
	track, needsTlang, target, err := pickTrackWithOriginal(tracks, orig, opts.Languages, opts.AllowTranslation)
	if err != nil {
		return nil, err
	}
	segs, err := downloadSegments(ctx, track.BaseURL, target, needsTlang)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		te := newToolError(CodeNoCaptions, "The caption track for %s is empty.", track.LanguageCode)
		te.Suggestion = "Retry or request a different language; the track may be unusable."
		return nil, te
	}
	language, originalLanguage, translationTarget, translated := transcriptIdentity(track, orig, needsTlang, target)
	return &Transcript{
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
// non-original ASR track is a machine translation served as a track. Requesting
// "es" on an English video must not report translated=false with
// original_language="es": that presents translated speech as the speaker's words.
func transcriptIdentity(track, original *captionTrack, needsTlang bool, target string) (language, originalLanguage, translationTarget string, translated bool) {
	language = track.LanguageCode
	originalLanguage = track.LanguageCode
	switch {
	case needsTlang:
		language = target
		translationTarget = target
		translated = true
	case original != nil:
		originalLanguage = original.LanguageCode
		if track.Kind == "asr" && !langMatches(track.LanguageCode, original.LanguageCode) {
			translated = true
			translationTarget = track.LanguageCode
		}
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
// (resolved from the player response). Passing nil preserves the legacy
// "first track" fallback used by tests and by responses without audio tracks.
func pickTrackWithOriginal(tracks []captionTrack, original *captionTrack, langs []string, allowTranslation bool) (*captionTrack, bool, string, error) {
	langs = normalizeLanguages(langs)
	for _, lang := range langs {
		if lang == OriginalLanguageValue {
			if original != nil {
				return original, false, "", nil
			}
			if t := originalTrack(tracks); t != nil {
				return t, false, "", nil
			}
			continue
		}
		for _, wantASR := range []bool{false, true} {
			for i := range tracks {
				t := &tracks[i]
				if (t.Kind == "asr") == wantASR && langMatches(t.LanguageCode, lang) {
					return t, false, "", nil
				}
			}
		}
	}
	if !allowTranslation {
		return nil, false, "", noMatchingLanguage(tracks, langs)
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
	return nil, false, "", noMatchingLanguage(tracks, langs)
}

// originalTrack returns the uploader's preferred track: a manual track if one
// exists, otherwise the first auto-generated track.
func originalTrack(tracks []captionTrack) *captionTrack {
	for i := range tracks {
		if tracks[i].Kind != "asr" {
			return &tracks[i]
		}
	}
	if len(tracks) > 0 {
		return &tracks[0]
	}
	return nil
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
// video). defaultAudioTrackIndex points at the original audio; its caption
// list names the matching caption track.
//
// It returns nil when the response carries no audio-track metadata. Callers
// that only need a usable track for the "original" sentinel can fall back to
// originalTrack(tracks); provenance code must not guess.
func originalTrackFrom(pr *playerResponse) *captionTrack {
	tracks := pr.tracks()
	if len(tracks) == 0 {
		return nil
	}
	r := pr.Captions.Renderer
	idx := r.DefaultAudioTrackIndex
	if len(r.AudioTracks) > 0 && idx >= 0 && idx < len(r.AudioTracks) {
		at := r.AudioTracks[idx]
		lang := at.AudioTrackID
		if i := strings.IndexByte(lang, '.'); i > 0 {
			lang = lang[:i]
		}
		if lang != "" {
			for _, wantASR := range []bool{false, true} {
				for i := range tracks {
					t := &tracks[i]
					if (t.Kind == "asr") == wantASR && langMatches(t.LanguageCode, lang) {
						return t
					}
				}
			}
		}
		if len(at.CaptionTrackIndices) > 0 {
			if ci := at.CaptionTrackIndices[0]; ci >= 0 && ci < len(tracks) {
				return &tracks[ci]
			}
		}
	}
	return nil
}

func noMatchingLanguage(tracks []captionTrack, langs []string) *ToolError {
	te := newToolError(CodeNoMatchingLang,
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
		return "", newToolError(CodeUpstreamChanged, "invalid caption URL: %v", err)
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
func downloadSegments(ctx context.Context, baseURL, targetLang string, translate bool) ([]Segment, error) {
	var errs []error
	for _, format := range []string{"json3", "srv3"} {
		u, err := captionURL(baseURL, format, targetLang, translate)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		resp, err := requestWithRetry(ctx, http.MethodGet, u, nil, map[string]string{
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
		var segs []Segment
		if format == "json3" {
			segs, err = parseJSON3(body)
		} else {
			segs, err = parseTimedXML(body)
		}
		if err != nil {
			errs = append(errs, newToolError(CodeUpstreamChanged, "caption %s parse failed: %v", format, err))
			continue
		}
		if len(segs) > 0 {
			return segs, nil
		}
	}
	if len(errs) == 0 {
		return nil, nil
	}
	return nil, aggregateToolErrors(errs)
}

// ---- json3 format ----

type json3Doc struct {
	Events []struct {
		StartMs    int64 `json:"tStartMs"`
		DurationMs int64 `json:"dDurationMs"`
		Segs       []struct {
			UTF8 string `json:"utf8"`
		} `json:"segs"`
	} `json:"events"`
}

func parseJSON3(body []byte) ([]Segment, error) {
	var doc json3Doc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var out []Segment
	for _, ev := range doc.Events {
		var b strings.Builder
		for _, s := range ev.Segs {
			b.WriteString(s.UTF8)
		}
		text := strings.Join(strings.Fields(b.String()), " ")
		if text == "" {
			continue
		}
		out = append(out, Segment{
			Start:    float64(ev.StartMs) / 1000,
			Duration: float64(ev.DurationMs) / 1000,
			Text:     text,
		})
	}
	deriveDurations(out)
	return out, nil
}

// deriveDurations backfills missing durations from the next cue's start so that
// block grouping and SRT output stay sane for both json3 and srv3 payloads.
func deriveDurations(segs []Segment) {
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

func parseTimedXML(body []byte) ([]Segment, error) {
	var doc timedXML
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var out []Segment
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
		out = append(out, Segment{
			Start:    float64(p.T) / 1000,
			Duration: float64(p.D) / 1000,
			Text:     text,
		})
	}
	// srv3 often omits per-paragraph durations; derive them from the next start.
	deriveDurations(out)
	return out, nil
}
