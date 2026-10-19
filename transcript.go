// Transcript model, track selection, and caption download/parsing.
package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var (
	videoIDPattern = regexp.MustCompile(
		`(?:youtube\.com/(?:watch\?[^#\s]*v=|shorts/|live/|embed/|v/)|youtu\.be/)([\w-]{11})`)
	bareIDPattern = regexp.MustCompile(`^[\w-]{11}$`)
)

func extractVideoID(urlOrID string) (string, error) {
	s := strings.TrimSpace(urlOrID)
	if bareIDPattern.MatchString(s) {
		return s, nil
	}
	if m := videoIDPattern.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("could not extract a YouTube video id from %q", urlOrID)
}

type Segment struct {
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
	Text     string  `json:"text"`
}

type Transcript struct {
	VideoID   string    `json:"video_id"`
	Source    string    `json:"source"` // innertube-android | innertube-ios | watch-page
	Language  string    `json:"language"`
	Generated bool      `json:"generated"`
	Translated bool     `json:"translated"`
	Segments  []Segment `json:"segments"`
}

// FetchTranscript cascades through the acquisition methods; within each
// method it also retries the caption download, since a client can hand out
// a track list but have its download URL throttled.
func FetchTranscript(urlOrID string, langs []string) (*Transcript, error) {
	videoID, err := extractVideoID(urlOrID)
	if err != nil {
		return nil, err
	}
	if len(langs) == 0 {
		langs = []string{"en"}
	}
	var errs []string
	for _, c := range clientContexts {
		pr, err := innertubePlayer(videoID, c)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		t, err := selectAndDownload(videoID, pr, langs)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", c.Name, err))
			continue
		}
		t.Source = "innertube-" + strings.ToLower(c.Name)
		return t, nil
	}
	pr, err := watchPagePlayer(videoID)
	if err != nil {
		errs = append(errs, err.Error())
	} else if t, err := selectAndDownload(videoID, pr, langs); err != nil {
		errs = append(errs, "watch-page: "+err.Error())
	} else {
		t.Source = "watch-page"
		return t, nil
	}
	return nil, fmt.Errorf("could not fetch transcript: %s", strings.Join(errs, " | "))
}

func selectAndDownload(videoID string, pr *playerResponse, langs []string) (*Transcript, error) {
	tracks := pr.tracks()
	if len(tracks) == 0 {
		return nil, fmt.Errorf("no caption tracks for %s", videoID)
	}
	track, translated := pickTrack(tracks, langs)
	if track == nil {
		return nil, fmt.Errorf("no usable caption track (have: %s)", trackList(tracks))
	}
	segs, err := downloadSegments(track.BaseURL, langs[0], translated)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("empty caption track")
	}
	return &Transcript{
		VideoID:   videoID,
		Language:  track.LanguageCode,
		Generated: track.Kind == "asr",
		Translated: translated,
		Segments:  segs,
	}, nil
}

// pickTrack prefers, in order: manual track in a requested language →
// auto-generated in a requested language → (prefix matches of the same) →
// any translatable track rendered into langs[0].
func pickTrack(tracks []captionTrack, langs []string) (*captionTrack, bool) {
	langMatch := func(code, lang string) bool {
		return code == lang || strings.HasPrefix(code, lang+"-")
	}
	for _, wantASR := range []bool{false, true} {
		for _, lang := range langs {
			for i := range tracks {
				t := &tracks[i]
				if (t.Kind == "asr") == wantASR && langMatch(t.LanguageCode, lang) {
					return t, false
				}
			}
		}
	}
	for _, wantASR := range []bool{false, true} {
		for i := range tracks {
			t := &tracks[i]
			if (t.Kind == "asr") == wantASR && t.IsTranslatable {
				return t, true
			}
		}
	}
	return nil, false
}

func trackList(tracks []captionTrack) string {
	var parts []string
	for _, t := range tracks {
		kind := "manual"
		if t.Kind == "asr" {
			kind = "asr"
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", t.LanguageCode, kind))
	}
	return strings.Join(parts, ", ")
}

// downloadSegments tries the json3 format first, then falls back to the
// legacy XML timedtext format.
func downloadSegments(baseURL, targetLang string, translate bool) ([]Segment, error) {
	u := baseURL
	if translate {
		u += "&tlang=" + targetLang
	}
	for _, fmtParam := range []string{"&fmt=json3", "&fmt=srv3"} {
		body, err := httpGet(u + fmtParam)
		if err != nil {
			return nil, err
		}
		var segs []Segment
		if strings.HasSuffix(fmtParam, "json3") {
			segs, err = parseJSON3(body)
		} else {
			segs, err = parseTimedXML(body)
		}
		if err == nil && len(segs) > 0 {
			return segs, nil
		}
	}
	return nil, fmt.Errorf("caption download returned no segments")
}

func httpGet(u string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
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
		text := strings.TrimSpace(b.String())
		if text == "" {
			continue
		}
		out = append(out, Segment{
			Start:    float64(ev.StartMs) / 1000,
			Duration: float64(ev.DurationMs) / 1000,
			Text:     text,
		})
	}
	return out, nil
}

// ---- legacy timedtext XML ----

type timedXML struct {
	Paras []struct {
		T    int64  `xml:"t,attr"`
		D    int64  `xml:"d,attr"`
		HTML string `xml:",innerxml"`
	} `xml:"body>p"`
}

var tagStripper = regexp.MustCompile(`<[^>]+>`)

func parseTimedXML(body []byte) ([]Segment, error) {
	var doc timedXML
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var out []Segment
	for _, p := range doc.Paras {
		text := strings.TrimSpace(html.UnescapeString(tagStripper.ReplaceAllString(p.HTML, "")))
		if text == "" {
			continue
		}
		out = append(out, Segment{
			Start:    float64(p.T) / 1000,
			Duration: float64(p.D) / 1000,
			Text:     text,
		})
	}
	return out, nil
}
