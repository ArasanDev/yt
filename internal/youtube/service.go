// Package youtube acquires video metadata, captions, and channel listings from
// YouTube's internal endpoints. It owns the cache and the typed failure model;
// rendering and search live in separate packages.
package youtube

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yt/internal/apperr"
	"yt/internal/transcript"
)

const (
	playerTTL     = 10 * time.Minute
	livePlayerTTL = 30 * time.Second
	// Caption URLs that came from the watch-page scrape are usually pot-gated;
	// don't hold that response for the full player TTL.
	watchPagePlayerTTL = 2 * time.Minute
	transcriptTTL      = 30 * time.Minute
	negativeTTL        = 60 * time.Second
	// Rate limits are cached briefly so an agent following retryable=true does
	// not hammer the endpoint that throttled it.
	rateLimitedTTL = 15 * time.Second
)

// TotalDeadline bounds one acquisition operation: player fetch plus caption
// download share it instead of each getting a fresh budget.
const TotalDeadline = 45 * time.Second

// DefaultDescriptionChars is the description excerpt used by the CLI and MCP
// adapter when the caller does not specify one.
const DefaultDescriptionChars = 500

// Config lets tests and embedders point acquisition at another origin.
type Config struct {
	InnertubeURL string
	WatchPageURL string
	ResolveURL   string
	FeedURL      string
	HTTPClient   *http.Client
}

// DefaultConfig returns the production endpoints.
func DefaultConfig() Config {
	return Config{
		InnertubeURL: "https://www.youtube.com/youtubei/v1/player?prettyPrint=false",
		WatchPageURL: "https://www.youtube.com/watch?v=",
		ResolveURL:   "https://www.youtube.com/youtubei/v1/navigation/resolve_url?prettyPrint=false",
		FeedURL:      "https://www.youtube.com/feeds/videos.xml?channel_id=",
		HTTPClient:   &http.Client{Timeout: perAttemptTimeout},
	}
}

// Client is a cached YouTube acquisition session.
type Client struct {
	memo *Memo
	cfg  Config
}

// NewClient returns a client using the production endpoints.
func NewClient() *Client { return NewClientWithConfig(DefaultConfig()) }

// NewClientWithConfig returns a client with any unset fields filled from
// DefaultConfig.
func NewClientWithConfig(cfg Config) *Client {
	def := DefaultConfig()
	if cfg.InnertubeURL == "" {
		cfg.InnertubeURL = def.InnertubeURL
	}
	if cfg.WatchPageURL == "" {
		cfg.WatchPageURL = def.WatchPageURL
	}
	if cfg.ResolveURL == "" {
		cfg.ResolveURL = def.ResolveURL
	}
	if cfg.FeedURL == "" {
		cfg.FeedURL = def.FeedURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = def.HTTPClient
	}
	return &Client{memo: NewMemo(32 << 20), cfg: cfg}
}

type playerCache struct {
	pr  *playerResponse
	via string
}

// Player returns a player response, memoized per video. Negative results
// (private, not found, no captions) are cached briefly so a probing agent
// cannot turn the improved error path into a hot loop.
func (c *Client) Player(ctx context.Context, videoID string) (*playerResponse, string, error) {
	if v, ok := c.memo.Get("neg:" + videoID); ok {
		if te, ok := v.(*apperr.Error); ok {
			return nil, "", te
		}
	}
	if v, ok := c.memo.Get("player:" + videoID); ok {
		pv := v.(playerCache)
		return pv.pr, pv.via, nil
	}
	ctx, cancel := context.WithTimeout(ctx, TotalDeadline)
	defer cancel()
	pr, via, err := c.fetchPlayer(ctx, videoID)
	if err != nil {
		if te, ok := apperr.As(err); ok {
			switch {
			case te.Code == apperr.CodeRateLimited:
				c.memo.Put("neg:"+videoID, te, rateLimitedTTL, 512)
			case !te.Retryable:
				c.memo.Put("neg:"+videoID, te, negativeTTL, 512)
			}
		}
		return nil, "", err
	}
	// A success invalidates any cached failure (e.g. a transient block).
	c.memo.Delete("neg:" + videoID)
	ttl := playerTTL
	switch {
	case pr.isLive():
		ttl = livePlayerTTL
	case via == "watch-page":
		ttl = watchPagePlayerTTL
	}
	c.memo.Put("player:"+videoID, playerCache{pr: pr, via: via}, ttl, estimatePlayerSize(pr))
	return pr, via, nil
}

// estimatePlayerSize is a conservative charge for a parsed player response:
// the raw JSON we measured is ~200KB, dominated by caption track URLs and the
// description. Under-charging would let the memo grow far past its byte bound.
func estimatePlayerSize(pr *playerResponse) int {
	size := 100 << 10
	size += len(pr.VideoDetails.ShortDescription)
	for _, t := range pr.tracks() {
		size += len(t.BaseURL) + 64
	}
	return size
}

// Transcript fetches and caches a caption track for a video.
func (c *Client) Transcript(ctx context.Context, videoID string, opts TranscriptOptions) (*transcript.Transcript, error) {
	opts.Languages = normalizeLanguages(opts.Languages)
	if len(opts.Languages) == 0 {
		opts.Languages = []string{"en"}
	}
	key := "tr:" + videoID + "|" + strings.Join(opts.Languages, ",") + "|" + strconv.FormatBool(opts.AllowTranslation)
	if v, ok := c.memo.Get(key); ok {
		return v.(*transcript.Transcript), nil
	}
	// One deadline for the whole call: player fetch + caption download must not
	// each get a fresh 45s budget.
	ctx, cancel := context.WithTimeout(ctx, TotalDeadline)
	defer cancel()
	pr, via, err := c.Player(ctx, videoID)
	if err != nil {
		return nil, err
	}
	t, err := c.selectAndDownload(ctx, videoID, pr, via, opts)
	if err != nil {
		return nil, err
	}
	if !pr.isLive() {
		c.memo.Put(key, t, transcriptTTL, transcriptSize(t))
	}
	return t, nil
}

type CaptionSummary struct {
	HasCaptions      bool     `json:"has_captions"`
	Manual           []string `json:"manual"`
	Auto             []string `json:"auto"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	Translatable     bool     `json:"translatable"`
	Count            int      `json:"count"`
}

type VideoInfo struct {
	OK                        bool           `json:"ok"`
	VideoID                   string         `json:"video_id"`
	Title                     string         `json:"title"`
	Channel                   string         `json:"channel"`
	ChannelID                 string         `json:"channel_id"`
	DurationSeconds           int            `json:"duration_seconds"`
	ViewCount                 int64          `json:"view_count"`
	IsLive                    bool           `json:"is_live"`
	PublishedAt               string         `json:"published_at,omitempty"`
	Category                  string         `json:"category,omitempty"`
	Keywords                  []string       `json:"keywords,omitempty"`
	Description               string         `json:"description"`
	DescriptionTruncated      bool           `json:"description_truncated"`
	Chapters                  []Chapter      `json:"chapters,omitempty"`
	Captions                  CaptionSummary `json:"captions"`
	EstimatedTranscriptChars  int            `json:"estimated_transcript_chars"`
	EstimatedTranscriptTokens int            `json:"estimated_transcript_tokens"`
	URL                       string         `json:"url"`
	Via                       string         `json:"via"`
}

// Info returns metadata without downloading a caption track.
func (c *Client) Info(ctx context.Context, videoID string, descriptionChars int) (*VideoInfo, error) {
	pr, via, err := c.Player(ctx, videoID)
	if err != nil {
		return nil, err
	}
	tracks := pr.tracks()
	summary := CaptionSummary{Manual: []string{}, Auto: []string{}, Count: len(tracks)}
	for _, t := range tracks {
		if t.Kind == "asr" {
			summary.Auto = append(summary.Auto, t.LanguageCode)
		} else {
			summary.Manual = append(summary.Manual, t.LanguageCode)
		}
		if t.IsTranslatable {
			summary.Translatable = true
		}
	}
	summary.Manual = uniqueStrings(summary.Manual)
	summary.Auto = uniqueStrings(summary.Auto)
	summary.HasCaptions = len(tracks) > 0
	if orig := originalTrackFrom(pr); orig != nil {
		summary.OriginalLanguage = orig.LanguageCode
	}

	views, _ := strconv.ParseInt(pr.VideoDetails.ViewCount, 10, 64)
	duration := pr.durationSeconds()
	// Parse chapters from the full description before truncation: a chapter
	// list often sits past the excerpt cut-off.
	chapters := parseChapters(pr.VideoDetails.ShortDescription, duration)
	desc, truncated := transcript.TruncateRunes(pr.VideoDetails.ShortDescription, descriptionChars)
	estimate := 0
	if summary.HasCaptions {
		estimate = duration * 17 // ~17 chars/second observed on real ASR
	}
	info := &VideoInfo{
		OK:                        true,
		VideoID:                   pr.VideoDetails.VideoID,
		Title:                     pr.VideoDetails.Title,
		Channel:                   pr.VideoDetails.Author,
		ChannelID:                 pr.VideoDetails.ChannelID,
		DurationSeconds:           duration,
		ViewCount:                 views,
		IsLive:                    pr.isLive(),
		PublishedAt:               pr.Microformat.Renderer.PublishDate,
		Category:                  pr.Microformat.Renderer.Category,
		Keywords:                  pr.Microformat.Renderer.Keywords,
		Description:               desc,
		DescriptionTruncated:      truncated,
		Chapters:                  chapters,
		Captions:                  summary,
		EstimatedTranscriptChars:  estimate,
		EstimatedTranscriptTokens: estimate / 4,
		URL:                       "https://www.youtube.com/watch?v=" + videoID,
		Via:                       via,
	}
	if info.VideoID == "" {
		info.VideoID = videoID
	}
	return info, nil
}

// Track is the public view of a caption track.
type Track struct {
	Language     string
	Kind         string // "asr" = auto-generated
	Translatable bool
	Name         string
}

// Tracks lists caption tracks and the acquisition source that produced them.
func (c *Client) Tracks(ctx context.Context, videoID string) (via string, tracks []Track, err error) {
	pr, via, err := c.Player(ctx, videoID)
	if err != nil {
		return "", nil, err
	}
	raw := pr.tracks()
	tracks = make([]Track, 0, len(raw))
	for _, t := range raw {
		tracks = append(tracks, Track{
			Language:     t.LanguageCode,
			Kind:         t.Kind,
			Translatable: t.IsTranslatable,
			Name:         t.trackName(),
		})
	}
	return via, tracks, nil
}

func transcriptSize(t *transcript.Transcript) int {
	n := 0
	for _, s := range t.Segments {
		n += len(s.Text) + 24 + 32
	}
	return n
}
