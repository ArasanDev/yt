// Innertube (youtubei) access — the same internal API the YouTube apps use.
//
// Since mid-2025, caption track URLs handed to WEB clients require a
// "proof of origin" token; ANDROID and IOS client contexts still receive
// directly-downloadable signed URLs. We therefore cascade through
// ANDROID → IOS → watch-page scrape so one blocked endpoint doesn't
// take the whole fetch down. Stdlib only.
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"yt/internal/apperr"
)

const (
	// Per-attempt HTTP timeout. A full cascade is bounded by the caller's
	// total deadline, not by this.
	perAttemptTimeout = 10 * time.Second
	maxAttempts       = 2
	maxBackoff        = 2 * time.Second
)

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

type audioTrack struct {
	AudioTrackID        string `json:"audioTrackId"`
	CaptionTrackIndices []int  `json:"captionTrackIndices"`
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
		IsLiveContent    bool   `json:"isLiveContent"`
	} `json:"videoDetails"`
	Captions struct {
		Renderer struct {
			CaptionTracks          []captionTrack `json:"captionTracks"`
			AudioTracks            []audioTrack   `json:"audioTracks"`
			DefaultAudioTrackIndex int            `json:"defaultAudioTrackIndex"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
	// May be absent in ANDROID/IOS responses; treated as optional metadata.
	Microformat struct {
		Renderer struct {
			PublishDate string   `json:"publishDate"`
			UploadDate  string   `json:"uploadDate"`
			Category    string   `json:"category"`
			Keywords    []string `json:"keywords,omitempty"`
		} `json:"playerMicroformatRenderer"`
	} `json:"microformat"`
}

func (p *playerResponse) tracks() []captionTrack {
	return p.Captions.Renderer.CaptionTracks
}

func (p *playerResponse) playable() bool {
	return p.PlayabilityStatus.Status == "OK"
}

func (p *playerResponse) isLive() bool {
	return p.VideoDetails.IsLiveContent
}

func (p *playerResponse) durationSeconds() int {
	n := 0
	for _, c := range strings.TrimSpace(p.VideoDetails.LengthSeconds) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ---- HTTP with bounded retries ----

func backoff(attempt int) time.Duration {
	d := 250 * time.Millisecond
	for i := 1; i < attempt; i++ {
		d *= 2
		if d > maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// requestWithRetry issues one logical request, retrying only on 429/5xx and
// network errors. It returns the response for any other status so callers can
// classify it (404 means something different than 403).
func (c *Client) requestWithRetry(ctx context.Context, method, url string, body []byte, headers map[string]string) (*http.Response, error) {
	var last error
	sleptForRetryAfter := false
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 && !sleptForRetryAfter {
			select {
			case <-ctx.Done():
				return nil, apperr.ClassifyContext(ctx.Err())
			case <-time.After(backoff(attempt)):
			}
		}
		sleptForRetryAfter = false
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return nil, apperr.New(apperr.CodeBadRequest, "could not build request: %v", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.cfg.HTTPClient.Do(req)
		if err != nil {
			if ce := apperr.ClassifyContext(ctx.Err()); ce != nil {
				return nil, ce
			}
			te := apperr.New(apperr.CodeNetwork, "network error: %v", err)
			te.Retryable = true
			last = te
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			te := apperr.ClassifyHTTPStatus(resp.StatusCode, resp.Header.Get("Retry-After"))
			resp.Body.Close()
			last = te
			if attempt == maxAttempts-1 {
				return nil, te
			}
			if te.RetryAfterSeconds > 0 {
				wait := time.Duration(te.RetryAfterSeconds) * time.Second
				if wait > maxBackoff*5 {
					wait = maxBackoff * 5
				}
				select {
				case <-ctx.Done():
					return nil, apperr.ClassifyContext(ctx.Err())
				case <-time.After(wait):
				}
				sleptForRetryAfter = true
			}
			continue
		}
		return resp, nil
	}
	if last == nil {
		last = apperr.New(apperr.CodeNetwork, "request failed")
	}
	return nil, last
}

func readBody(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apperr.ClassifyHTTPStatus(resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// ---- fetchers ----

func clientContextBody(c clientContext) map[string]any {
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
	return client
}

func (c *Client) innertubePlayer(ctx context.Context, videoID string, cc clientContext) (*playerResponse, error) {
	body, err := json.Marshal(map[string]any{
		"context": map[string]any{"client": clientContextBody(cc)},
		"videoId": videoID,
	})
	if err != nil {
		return nil, apperr.New(apperr.CodeInternal, "could not encode request: %v", err)
	}
	resp, err := c.requestWithRetry(ctx, http.MethodPost, c.cfg.InnertubeURL, body, map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   cc.UA,
	})
	if err != nil {
		return nil, err
	}
	raw, err := readBody(resp, 16<<20)
	if err != nil {
		return nil, err
	}
	var pr playerResponse
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, apperr.New(apperr.CodeUpstreamChanged, "innertube %s returned unparsable JSON: %v", cc.Name, err)
	}
	if !pr.playable() {
		return nil, apperr.ClassifyPlayability(pr.PlayabilityStatus.Status, pr.PlayabilityStatus.Reason)
	}
	return &pr, nil
}

// watchPagePlayer scrapes ytInitialPlayerResponse out of the HTML watch page.
// Kept as last resort: its caption URLs are pot-gated since mid-2025, but the
// track list itself is still useful when the youtubei POST endpoint is blocked.
func (c *Client) watchPagePlayer(ctx context.Context, videoID string) (*playerResponse, error) {
	resp, err := c.requestWithRetry(ctx, http.MethodGet, c.cfg.WatchPageURL+videoID, nil, map[string]string{
		"User-Agent":      browserUA,
		"Accept-Language": "en-US,en;q=0.9",
	})
	if err != nil {
		return nil, err
	}
	page, err := readBody(resp, 32<<20)
	if err != nil {
		return nil, err
	}
	raw := extractInitialPlayerResponse(string(page))
	if raw == "" {
		return nil, apperr.New(apperr.CodeUpstreamChanged, "watch page did not contain ytInitialPlayerResponse")
	}
	var pr playerResponse
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		return nil, apperr.New(apperr.CodeUpstreamChanged, "watch page player response was unparsable: %v", err)
	}
	if !pr.playable() {
		return nil, apperr.ClassifyPlayability(pr.PlayabilityStatus.Status, pr.PlayabilityStatus.Reason)
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

// fetchPlayer cascades acquisition methods. A response whose caption track list
// is empty is only used as a fallback: an OK response with zero tracks from one
// client must not short-circuit clients that do have tracks.
func (c *Client) fetchPlayer(ctx context.Context, videoID string) (*playerResponse, string, error) {
	var errs []error
	var fallback *playerResponse
	var fallbackVia string

	for _, cc := range clientContexts {
		pr, err := c.innertubePlayer(ctx, videoID, cc)
		if err != nil {
			if te, ok := apperr.As(err); ok {
				switch te.Code {
				case apperr.CodePrivate, apperr.CodeAgeRestricted, apperr.CodeBotCheck, apperr.CodeNotFound:
					return nil, "", te
				}
			}
			errs = append(errs, err)
			continue
		}
		via := "innertube-" + strings.ToLower(cc.Name)
		if len(pr.tracks()) > 0 {
			return pr, via, nil
		}
		if fallback == nil {
			fallback, fallbackVia = pr, via
		}
	}

	pr, err := c.watchPagePlayer(ctx, videoID)
	if err != nil {
		if te, ok := apperr.As(err); ok {
			switch te.Code {
			case apperr.CodePrivate, apperr.CodeAgeRestricted, apperr.CodeBotCheck, apperr.CodeNotFound:
				return nil, "", te
			}
		}
		errs = append(errs, err)
	} else if len(pr.tracks()) > 0 {
		return pr, "watch-page", nil
	} else if fallback == nil {
		fallback, fallbackVia = pr, "watch-page"
	}

	if fallback != nil {
		return fallback, fallbackVia, nil
	}
	return nil, "", apperr.Aggregate(errs)
}
