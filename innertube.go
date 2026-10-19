// Innertube (youtubei) access — the same internal API the YouTube apps use.
//
// Since mid-2025, caption track URLs handed to WEB clients require a
// "proof of origin" token; ANDROID and IOS client contexts still receive
// directly-downloadable signed URLs. We therefore cascade through
// ANDROID → IOS → watch-page scrape so one blocked endpoint doesn't
// take the whole fetch down. Stdlib only.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const innertubeURL = "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"

var httpClient = &http.Client{Timeout: 20 * time.Second}

type clientContext struct {
	Name       string // Innertube clientName
	Version    string // clientVersion
	UA         string // User-Agent header
	AndroidSDK int    // androidSdkVersion (ANDROID only)
	IOSModel   string // deviceModel (IOS only)
}

// Order matters: ANDROID first, IOS as backup.
var clientContexts = []clientContext{
	{
		Name: "ANDROID", Version: "20.10.38", AndroidSDK: 30,
		UA: "com.google.android.youtube/20.10.38 (Linux; U; Android 14)",
	},
	{
		Name: "IOS", Version: "20.10.4", IOSModel: "iPhone16,2",
		UA: "com.google.ios.youtube/20.10.4 (iPhone16,2; U; CPU iOS 18_4 like Mac OS X)",
	},
}

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// ---- response types (only the fields we consume) ----

type captionTrack struct {
	BaseURL        string `json:"baseUrl"`
	LanguageCode   string `json:"languageCode"`
	Kind           string `json:"kind"` // "asr" = auto-generated
	IsTranslatable bool   `json:"isTranslatable"`
	Name           struct {
		SimpleText string `json:"simpleText"`
		Runs       []struct {
			Text string `json:"text"`
		} `json:"runs"`
	} `json:"name"`
}

func (t captionTrack) trackName() string {
	if t.Name.SimpleText != "" {
		return t.Name.SimpleText
	}
	var b strings.Builder
	for _, r := range t.Name.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

type playerResponse struct {
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	VideoDetails struct {
		VideoID          string `json:"videoId"`
		Title            string `json:"title"`
		Author           string `json:"author"`
		ChannelID        string `json:"channelId"`
		LengthSeconds    string `json:"lengthSeconds"`
		ViewCount        string `json:"viewCount"`
		ShortDescription string `json:"shortDescription"`
	} `json:"videoDetails"`
	Captions struct {
		Renderer struct {
			CaptionTracks []captionTrack `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
}

func (p *playerResponse) tracks() []captionTrack {
	return p.Captions.Renderer.CaptionTracks
}

func (p *playerResponse) playable() bool {
	return p.PlayabilityStatus.Status == "OK"
}

// ---- fetchers ----

func innertubePlayer(videoID string, c clientContext) (*playerResponse, error) {
	client := map[string]any{
		"clientName":    c.Name,
		"clientVersion": c.Version,
		"hl":            "en",
	}
	if c.AndroidSDK != 0 {
		client["androidSdkVersion"] = c.AndroidSDK
	}
	if c.IOSModel != "" {
		client["deviceMake"] = "Apple"
		client["deviceModel"] = c.IOSModel
	}
	body, err := json.Marshal(map[string]any{
		"context": map[string]any{"client": client},
		"videoId": videoID,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, innertubeURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.UA)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("innertube %s: HTTP %d", c.Name, resp.StatusCode)
	}
	var pr playerResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&pr); err != nil {
		return nil, fmt.Errorf("innertube %s: %w", c.Name, err)
	}
	if !pr.playable() {
		return nil, fmt.Errorf("innertube %s: %s: %s",
			c.Name, pr.PlayabilityStatus.Status, pr.PlayabilityStatus.Reason)
	}
	return &pr, nil
}

// watchPagePlayer scrapes ytInitialPlayerResponse out of the HTML watch page.
// Kept as last resort: its caption URLs are pot-gated since mid-2025, but the
// track list itself is still useful when the youtubei POST endpoint is blocked.
func watchPagePlayer(videoID string) (*playerResponse, error) {
	u := "https://www.youtube.com/watch?v=" + videoID
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("watch page: HTTP %d", resp.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	raw := extractInitialPlayerResponse(string(page))
	if raw == "" {
		return nil, fmt.Errorf("watch page: ytInitialPlayerResponse not found")
	}
	var pr playerResponse
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		return nil, fmt.Errorf("watch page: %w", err)
	}
	if !pr.playable() {
		return nil, fmt.Errorf("watch page: %s: %s",
			pr.PlayabilityStatus.Status, pr.PlayabilityStatus.Reason)
	}
	return &pr, nil
}

// extractInitialPlayerResponse finds `ytInitialPlayerResponse = {…}` in the
// page and returns the balanced JSON object (regex alone can't do this).
func extractInitialPlayerResponse(page string) string {
	idx := strings.Index(page, "ytInitialPlayerResponse")
	if idx < 0 {
		return ""
	}
	rest := page[idx:]
	start := strings.IndexByte(rest, '{')
	if start < 0 {
		return ""
	}
	rest = rest[start:]
	depth, inStr, esc := 0, false, false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case !inStr && c == '{':
			depth++
		case !inStr && c == '}':
			depth--
			if depth == 0 {
				return rest[:i+1]
			}
		}
	}
	return ""
}

// getPlayerResponse returns the first working player response and which
// method produced it ("innertube-android", "innertube-ios", "watch-page").
func getPlayerResponse(videoID string) (*playerResponse, string, error) {
	var errs []string
	for _, c := range clientContexts {
		pr, err := innertubePlayer(videoID, c)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		return pr, "innertube-" + strings.ToLower(c.Name), nil
	}
	pr, err := watchPagePlayer(videoID)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		return pr, "watch-page", nil
	}
	return nil, "", fmt.Errorf("all methods failed: %s", strings.Join(errs, " | "))
}
