// Channel discovery: resolve a handle/URL to a channel id, then list recent
// uploads from the channel's RSS feed.
//
// RSS is the backbone because it carries exact publish timestamps. The richer
// `browse` grid also has duration and views but only relative times ("1 hour
// ago"), which cannot serve as a watermark for "what changed since last check".
package youtube

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"yt/internal/apperr"
	"yt/internal/transcript"
)

const (
	// feedMaxEntries is what YouTube's channel RSS exposes; there is no way to
	// ask for more without switching to the (relative-timestamp) browse API.
	feedMaxEntries      = 15
	DefaultChannelLimit = 10
	DefaultSummaryChars = 200

	channelIDTTL   = 24 * time.Hour
	channelFeedTTL = 5 * time.Minute
)

var handlePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{2,100}$`)

// Non-channel paths on youtube.com; passing one of these is a user mistake
// worth naming, not a resolution failure.
var nonChannelPaths = map[string]bool{
	"watch": true, "shorts": true, "embed": true, "live": true, "playlist": true,
	"results": true, "feed": true, "hashtag": true, "clip": true, "post": true,
	"attribution_link": true, "redirect": true,
}

type ChannelInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Handle string `json:"handle,omitempty"`
	URL    string `json:"url"`
}

type ChannelVideo struct {
	VideoID   string `json:"video_id"`
	Title     string `json:"title"`
	Published string `json:"published"` // RFC3339, UTC
	URL       string `json:"url"`
	Summary   string `json:"summary,omitempty"`
}

type ChannelListing struct {
	OK              bool           `json:"ok"`
	Channel         ChannelInfo    `json:"channel"`
	Videos          []ChannelVideo `json:"videos"`
	Count           int            `json:"count"`
	Available       int            `json:"available"`
	NewestPublished string         `json:"newest_published,omitempty"`
	Truncated       bool           `json:"truncated"`
	Notes           []string       `json:"notes,omitempty"`
}

// ChannelOptions are resolved at the CLI/MCP boundary: Limit 0 means "all"
// (the feed cap), SummaryChars 0 means "omit the description excerpt".
type ChannelOptions struct {
	Limit        int
	Since        time.Time
	SummaryChars int
}

type channelRef struct {
	id     string
	url    string
	handle string
	input  string
}

type resolvedChannel struct {
	id     string
	handle string
}

// ---- RSS types ----

type rssFeed struct {
	Title   string     `xml:"title"`
	Entries []rssEntry `xml:"entry"`
}

type rssEntry struct {
	VideoID string `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
	Title   string `xml:"title"`
	// Published is RFC3339, e.g. 2026-10-19T19:00:17+00:00.
	Published string `xml:"published"`
	Link      struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
	Group struct {
		Description string `xml:"description"`
	} `xml:"http://search.yahoo.com/mrss/ group"`
}

// ---- input parsing ----

func isChannelID(s string) bool {
	if len(s) != 24 || !strings.HasPrefix(s, "UC") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func notAChannel(what string) *apperr.Error {
	te := apperr.New(apperr.CodeNotAChannel, "That is a %s, not a channel.", what)
	te.Suggestion = "Pass a channel handle (@name), a channel URL (youtube.com/@name or /channel/UC…), or a bare UC… channel id."
	return te
}

// parseChannelRef turns user input into either a channel id or a URL to
// resolve. A bare token (no scheme, no @) is treated as a handle, which is the
// common case from a chat message.
func parseChannelRef(input string) (channelRef, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return channelRef{}, apperr.New(apperr.CodeBadRequest, "channel is required")
	}
	if isChannelID(s) {
		return channelRef{id: s}, nil
	}
	if strings.HasPrefix(s, "@") {
		h := strings.SplitN(s[1:], "/", 2)[0]
		if !handlePattern.MatchString(h) {
			return channelRef{}, notAChannel("malformed handle")
		}
		return channelRef{url: "https://www.youtube.com/@" + h, handle: h}, nil
	}
	if !strings.Contains(s, "://") && strings.Contains(s, "youtube.com") {
		s = "https://" + s
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return channelRef{}, notAChannel("URL that could not be parsed")
		}
		host := strings.ToLower(u.Hostname())
		host = strings.TrimPrefix(host, "www.")
		if host == "youtu.be" {
			// youtu.be is always a video short-link.
			return channelRef{}, notAChannel("video link")
		}
		switch host {
		case "youtube.com", "m.youtube.com":
		default:
			return channelRef{}, notAChannel("non-YouTube URL")
		}
		segs := splitPath(u.Path)
		if len(segs) == 0 {
			return channelRef{}, notAChannel("channel-less YouTube URL")
		}
		switch segs[0] {
		case "channel":
			if len(segs) < 2 || !isChannelID(segs[1]) {
				return channelRef{}, notAChannel("channel URL without a UC… id")
			}
			return channelRef{id: segs[1]}, nil
		case "c", "user":
			if len(segs) < 2 {
				return channelRef{}, notAChannel("incomplete channel URL")
			}
			return channelRef{url: "https://www.youtube.com/" + segs[0] + "/" + segs[1]}, nil
		default:
			if nonChannelPaths[segs[0]] {
				return channelRef{}, notAChannel("video or search URL")
			}
			if strings.HasPrefix(segs[0], "@") {
				h := segs[0][1:]
				if !handlePattern.MatchString(h) {
					return channelRef{}, notAChannel("malformed handle")
				}
				return channelRef{url: "https://www.youtube.com/@" + h, handle: h}, nil
			}
			return channelRef{}, notAChannel("YouTube URL that is not a channel")
		}
	}
	if !handlePattern.MatchString(s) {
		return channelRef{}, notAChannel("string that is not a handle")
	}
	return channelRef{url: "https://www.youtube.com/@" + s, handle: s}, nil
}

func splitPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// ---- fetching ----

// resolveChannel returns the channel id for a ref, memoized. Direct ids skip
// the network entirely.
func (c *Client) resolveChannel(ctx context.Context, ref channelRef) (id, handle string, err error) {
	if ref.id != "" {
		return ref.id, ref.handle, nil
	}
	key := "chid:" + strings.ToLower(ref.url)
	if v, ok := c.memo.Get(key); ok {
		r := v.(resolvedChannel)
		return r.id, r.handle, nil
	}
	cc := clientContexts[0]
	body, err := json.Marshal(map[string]any{
		"context": map[string]any{"client": clientContextBody(cc)},
		"url":     ref.url,
	})
	if err != nil {
		return "", "", apperr.New(apperr.CodeInternal, "could not encode channel resolve request: %v", err)
	}
	resp, err := c.requestWithRetry(ctx, http.MethodPost, c.cfg.ResolveURL, body, map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   cc.UA,
	})
	if err != nil {
		return "", "", err
	}
	raw, err := readBody(resp, 4<<20)
	if err != nil {
		return "", "", err
	}
	var rr struct {
		Endpoint struct {
			BrowseEndpoint struct {
				BrowseID string `json:"browseId"`
			} `json:"browseEndpoint"`
		} `json:"endpoint"`
	}
	if err := json.Unmarshal(raw, &rr); err != nil {
		te := apperr.New(apperr.CodeUpstreamChanged, "channel resolve response was not parsable JSON: %v", err)
		te.Suggestion = "Retry once; if it persists, YouTube may have changed the endpoint."
		return "", "", te
	}
	id = rr.Endpoint.BrowseEndpoint.BrowseID
	if id == "" {
		shown := ref.input
		if shown == "" {
			shown = ref.url
		}
		te := apperr.New(apperr.CodeChannelNotFound, "No YouTube channel matches %q.", shown)
		te.Suggestion = "Check the handle spelling, or pass the channel URL from a browser."
		return "", "", te
	}
	handle = ref.handle
	c.memo.Put(key, resolvedChannel{id: id, handle: handle}, channelIDTTL, 256)
	return id, handle, nil
}

// channelFeed fetches and parses the channel's Atom feed, memoized briefly so
// a burst of calls (info + list + archive sync) shares one request.
func (c *Client) channelFeed(ctx context.Context, id string) (*rssFeed, error) {
	key := "chfeed:" + id
	if v, ok := c.memo.Get(key); ok {
		return v.(*rssFeed), nil
	}
	resp, err := c.requestWithRetry(ctx, http.MethodGet, c.cfg.FeedURL+url.QueryEscape(id), nil, map[string]string{
		"User-Agent":      browserUA,
		"Accept":          "application/atom+xml, application/xml;q=0.9, */*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	})
	if err != nil {
		return nil, err
	}
	raw, err := readBody(resp, 4<<20)
	if err != nil {
		if te, ok := apperr.As(err); ok && te.Code == apperr.CodeNotFound {
			te = apperr.New(apperr.CodeChannelNotFound, "No YouTube channel or feed exists for id %s.", id)
			te.Suggestion = "Check the handle or channel URL."
			return nil, te
		}
		return nil, err
	}
	var f rssFeed
	if err := xml.Unmarshal(raw, &f); err != nil {
		te := apperr.New(apperr.CodeUpstreamChanged, "channel feed for %s was not parsable XML: %v", id, err)
		te.Suggestion = "Retry once; YouTube may have changed the feed format."
		return nil, te
	}
	c.memo.Put(key, &f, channelFeedTTL, len(raw)+256)
	return &f, nil
}

// Channel lists a channel's recent uploads, newest first.
func (c *Client) Channel(ctx context.Context, input string, opts ChannelOptions) (*ChannelListing, error) {
	ref, err := parseChannelRef(input)
	if err != nil {
		return nil, err
	}
	ref.input = strings.TrimSpace(input)
	ctx, cancel := context.WithTimeout(ctx, TotalDeadline)
	defer cancel()
	id, handle, err := c.resolveChannel(ctx, ref)
	if err != nil {
		return nil, err
	}
	feed, err := c.channelFeed(ctx, id)
	if err != nil {
		return nil, err
	}
	return buildChannelListing(id, handle, feed, opts), nil
}

func buildChannelListing(id, handle string, feed *rssFeed, opts ChannelOptions) *ChannelListing {
	limit := opts.Limit
	if limit <= 0 {
		limit = feedMaxEntries
	}

	info := ChannelInfo{
		ID:     id,
		Title:  cleanText(feed.Title),
		Handle: handle,
		URL:    "https://www.youtube.com/channel/" + id,
	}
	if handle != "" {
		info.URL = "https://www.youtube.com/@" + handle
	}
	if info.Title == "" {
		info.Title = id
	}

	type entry struct {
		video     ChannelVideo
		published time.Time
	}
	var entries []entry
	skipped := 0
	seen := make(map[string]bool, len(feed.Entries))
	for _, e := range feed.Entries {
		if e.VideoID == "" || seen[e.VideoID] {
			continue
		}
		seen[e.VideoID] = true
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(e.Published))
		if err != nil {
			skipped++
			continue
		}
		v := ChannelVideo{
			VideoID:   e.VideoID,
			Title:     cleanText(e.Title),
			Published: t.UTC().Format(time.RFC3339),
			URL:       "https://www.youtube.com/watch?v=" + e.VideoID,
		}
		if opts.SummaryChars > 0 {
			v.Summary, _ = transcript.TruncateRunes(cleanText(e.Group.Description), opts.SummaryChars)
		}
		entries = append(entries, entry{video: v, published: t})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].published.After(entries[j].published) })

	listing := &ChannelListing{OK: true, Channel: info, Videos: []ChannelVideo{}}
	if len(entries) > 0 {
		listing.NewestPublished = entries[0].video.Published
	}
	if skipped > 0 {
		listing.Notes = append(listing.Notes,
			fmt.Sprintf("skipped %d feed entries with an unparsable published time", skipped))
	}

	filtered := entries
	if !opts.Since.IsZero() {
		filtered = make([]entry, 0, len(entries))
		for _, e := range entries {
			if e.published.After(opts.Since) {
				filtered = append(filtered, e)
			}
		}
		if len(filtered) == 0 {
			listing.Notes = append(listing.Notes,
				fmt.Sprintf("no videos published after %s", opts.Since.UTC().Format(time.RFC3339)))
		}
	}
	listing.Available = len(filtered)

	if len(filtered) > limit {
		filtered = filtered[:limit]
		listing.Truncated = true
	}
	for _, e := range filtered {
		listing.Videos = append(listing.Videos, e.video)
	}
	listing.Count = len(listing.Videos)

	if len(feed.Entries) >= feedMaxEntries {
		listing.Notes = append(listing.Notes,
			fmt.Sprintf("the RSS feed exposes at most %d entries; older uploads are not listed", feedMaxEntries))
	}
	return listing
}

func cleanText(s string) string { return strings.Join(strings.Fields(s), " ") }

// ParseSince accepts RFC3339 or a bare date (UTC midnight).
func ParseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	te := apperr.New(apperr.CodeBadRequest, "invalid since %q", s)
	te.Suggestion = "Use RFC3339 (2026-10-01T00:00:00Z) or a date (2026-10-01); times are UTC."
	return time.Time{}, te
}
