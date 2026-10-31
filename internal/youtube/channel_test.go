package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArasanDev/yt/internal/apperr"
)

type fakeChannelServer struct {
	server       *httptest.Server
	client       *Client
	resolveCalls int32
	feedCalls    int32
	browseID     string
	feedXML      string
	feedStatus   int
	resolveBody  string
}

func newFakeChannel(t *testing.T) (*fakeChannelServer, func()) {
	t.Helper()
	f := &fakeChannelServer{browseID: "UCtesttesttesttesttest", feedStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/resolve", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.resolveCalls, 1)
		if f.resolveBody != "" {
			fmt.Fprint(w, f.resolveBody)
			return
		}
		fmt.Fprintf(w, `{"endpoint":{"browseEndpoint":{"browseId":%q}}}`, f.browseID)
	})
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.feedCalls, 1)
		if f.feedStatus != http.StatusOK {
			w.WriteHeader(f.feedStatus)
			return
		}
		fmt.Fprint(w, f.feedXML)
	})
	srv := httptest.NewServer(mux)
	f.server = srv
	cfg := DefaultConfig()
	cfg.ResolveURL = srv.URL + "/resolve"
	cfg.FeedURL = srv.URL + "/feed?channel_id="
	f.client = NewClientWithConfig(cfg)
	return f, srv.Close
}

// makeFeed builds an Atom feed with n entries, one hour apart, newest = base+n-1.
func makeFeed(title string, n int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom" xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns:media="http://search.yahoo.com/mrss/">`)
	fmt.Fprintf(&b, "<title>%s</title>", title)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("vid%08d", i)
		ts := base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		fmt.Fprintf(&b, `<entry><yt:videoId>%s</yt:videoId><title>Video %d</title><published>%s</published>`+
			`<link rel="alternate" href="https://www.youtube.com/watch?v=%s"/>`+
			`<media:group><media:description>Description %d</media:description></media:group></entry>`,
			id, i, ts, id, i)
	}
	b.WriteString("</feed>")
	return b.String()
}

func TestParseChannelRef(t *testing.T) {
	cases := []struct {
		in         string
		wantID     string
		wantURL    string
		wantHandle string
		wantCode   apperr.Code
	}{
		{in: "@aiDotEngineer", wantURL: "https://www.youtube.com/@aiDotEngineer", wantHandle: "aiDotEngineer"},
		{in: "aiDotEngineer", wantURL: "https://www.youtube.com/@aiDotEngineer", wantHandle: "aiDotEngineer"},
		{in: "@aiDotEngineer/videos", wantURL: "https://www.youtube.com/@aiDotEngineer", wantHandle: "aiDotEngineer"},
		{in: "https://www.youtube.com/@aiDotEngineer", wantURL: "https://www.youtube.com/@aiDotEngineer", wantHandle: "aiDotEngineer"},
		{in: "https://www.youtube.com/@aiDotEngineer/streams?x=1", wantURL: "https://www.youtube.com/@aiDotEngineer", wantHandle: "aiDotEngineer"},
		{in: "youtube.com/@xy/y", wantURL: "https://www.youtube.com/@xy", wantHandle: "xy"},
		{in: "UCG6qpjVnBTTT8wLGBygANOQ", wantID: "UCG6qpjVnBTTT8wLGBygANOQ"},
		{in: "https://www.youtube.com/channel/UCG6qpjVnBTTT8wLGBygANOQ", wantID: "UCG6qpjVnBTTT8wLGBygANOQ"},
		{in: "https://www.youtube.com/c/SomeName", wantURL: "https://www.youtube.com/c/SomeName"},
		{in: "https://www.youtube.com/user/SomeName", wantURL: "https://www.youtube.com/user/SomeName"},
		{in: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", wantCode: apperr.CodeNotAChannel},
		{in: "https://www.youtube.com/shorts/dQw4w9WgXcQ", wantCode: apperr.CodeNotAChannel},
		{in: "https://youtu.be/dQw4w9WgXcQ", wantCode: apperr.CodeNotAChannel},
		{in: "https://example.com/@x", wantCode: apperr.CodeNotAChannel},
		{in: "https://www.youtube.com/", wantCode: apperr.CodeNotAChannel},
		{in: "", wantCode: apperr.CodeBadRequest},
	}
	for _, tc := range cases {
		got, err := parseChannelRef(tc.in)
		if tc.wantCode != "" {
			te, ok := apperr.As(err)
			if !ok || te.Code != tc.wantCode {
				t.Errorf("%q: want code %s, got %v", tc.in, tc.wantCode, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.in, err)
			continue
		}
		if got.id != tc.wantID || got.url != tc.wantURL || got.handle != tc.wantHandle {
			t.Errorf("%q: got id=%q url=%q handle=%q, want id=%q url=%q handle=%q",
				tc.in, got.id, got.url, got.handle, tc.wantID, tc.wantURL, tc.wantHandle)
		}
	}
}

func TestChannelListingNewestFirstAndSince(t *testing.T) {
	f, cleanup := newFakeChannel(t)
	defer cleanup()
	f.feedXML = makeFeed("AI Engineer", 3) // newest = 2026-10-01T14:00:00Z

	c := f.client
	got, err := c.Channel(context.Background(), "@chan", ChannelOptions{Limit: 10, SummaryChars: 200})
	if err != nil {
		t.Fatal(err)
	}
	if got.Channel.Title != "AI Engineer" || got.Channel.Handle != "chan" || got.Channel.ID != f.browseID {
		t.Fatalf("unexpected channel: %+v", got.Channel)
	}
	if got.Count != 3 || got.Available != 3 || got.Truncated {
		t.Fatalf("count=%d available=%d truncated=%v", got.Count, got.Available, got.Truncated)
	}
	if got.NewestPublished != "2026-10-01T14:00:00Z" {
		t.Fatalf("newest = %q", got.NewestPublished)
	}
	if got.Videos[0].Title != "Video 2" || got.Videos[2].Title != "Video 0" {
		t.Fatalf("not newest-first: %q .. %q", got.Videos[0].Title, got.Videos[2].Title)
	}
	if got.Videos[0].Summary == "" {
		t.Fatal("expected description excerpt")
	}

	// Strictly-after: a watermark equal to the newest entry must return nothing.
	since, _ := time.Parse(time.RFC3339, "2026-10-01T14:00:00Z")
	got, err = c.Channel(context.Background(), "@chan", ChannelOptions{Limit: 10, Since: since, SummaryChars: 200})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 0 || got.Available != 0 {
		t.Fatalf("watermark equal to newest must return nothing, got %d", got.Count)
	}
	if got.NewestPublished != "2026-10-01T14:00:00Z" {
		t.Fatalf("watermark should still report newest, got %q", got.NewestPublished)
	}

	// A watermark at the second-newest returns exactly the newest.
	since, _ = time.Parse(time.RFC3339, "2026-10-01T13:00:00Z")
	got, err = c.Channel(context.Background(), "@chan", ChannelOptions{Limit: 10, Since: since, SummaryChars: 200})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Videos[0].Title != "Video 2" {
		t.Fatalf("since filter wrong: %+v", got.Videos)
	}
}

func TestChannelLimitTruncates(t *testing.T) {
	f, cleanup := newFakeChannel(t)
	defer cleanup()
	f.feedXML = makeFeed("Chan", 3)

	got, err := f.client.Channel(context.Background(), "@chan", ChannelOptions{Limit: 2, SummaryChars: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 || got.Available != 3 || !got.Truncated {
		t.Fatalf("count=%d available=%d truncated=%v", got.Count, got.Available, got.Truncated)
	}
	if got.Videos[0].Summary != "" {
		t.Fatal("SummaryChars=0 must omit excerpts")
	}
}

func TestChannelMemoizesResolveAndFeed(t *testing.T) {
	f, cleanup := newFakeChannel(t)
	defer cleanup()
	f.feedXML = makeFeed("Chan", 2)

	c := f.client
	for i := 0; i < 2; i++ {
		if _, err := c.Channel(context.Background(), "@chan", ChannelOptions{Limit: 10}); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&f.resolveCalls); got != 1 {
		t.Fatalf("want 1 resolve call, got %d", got)
	}
	if got := atomic.LoadInt32(&f.feedCalls); got != 1 {
		t.Fatalf("want 1 feed call, got %d", got)
	}
}

func TestChannelDirectIDSkipsResolve(t *testing.T) {
	f, cleanup := newFakeChannel(t)
	defer cleanup()
	f.feedXML = makeFeed("Chan", 1)

	got, err := f.client.Channel(context.Background(), "UCG6qpjVnBTTT8wLGBygANOQ", ChannelOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&f.resolveCalls) != 0 {
		t.Fatal("a direct UC id must not hit the resolve endpoint")
	}
	if got.Channel.ID != "UCG6qpjVnBTTT8wLGBygANOQ" {
		t.Fatalf("channel id = %q", got.Channel.ID)
	}
}

func TestChannelErrors(t *testing.T) {
	t.Run("unknown handle", func(t *testing.T) {
		f, cleanup := newFakeChannel(t)
		defer cleanup()
		f.browseID = ""
		_, err := f.client.Channel(context.Background(), "@nope", ChannelOptions{})
		te, ok := apperr.As(err)
		if !ok || te.Code != apperr.CodeChannelNotFound {
			t.Fatalf("want channel_not_found, got %v", err)
		}
	})
	t.Run("feed 404", func(t *testing.T) {
		f, cleanup := newFakeChannel(t)
		defer cleanup()
		f.feedStatus = http.StatusNotFound
		_, err := f.client.Channel(context.Background(), "UCG6qpjVnBTTT8wLGBygANOQ", ChannelOptions{})
		te, ok := apperr.As(err)
		if !ok || te.Code != apperr.CodeChannelNotFound {
			t.Fatalf("want channel_not_found, got %v", err)
		}
	})
	t.Run("unparsable feed", func(t *testing.T) {
		f, cleanup := newFakeChannel(t)
		defer cleanup()
		f.feedXML = "<feed><entry>"
		_, err := f.client.Channel(context.Background(), "UCG6qpjVnBTTT8wLGBygANOQ", ChannelOptions{})
		te, ok := apperr.As(err)
		if !ok || te.Code != apperr.CodeUpstreamChanged {
			t.Fatalf("want upstream_changed, got %v", err)
		}
	})
	t.Run("empty feed", func(t *testing.T) {
		f, cleanup := newFakeChannel(t)
		defer cleanup()
		f.feedXML = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Quiet</title></feed>`
		got, err := f.client.Channel(context.Background(), "UCG6qpjVnBTTT8wLGBygANOQ", ChannelOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Count != 0 || got.NewestPublished != "" || got.Videos == nil {
			t.Fatalf("empty feed: %+v", got)
		}
	})
}

func TestParseSince(t *testing.T) {
	got, err := ParseSince("2026-10-01T00:00:00Z")
	if err != nil || got.Format(time.RFC3339) != "2026-10-01T00:00:00Z" {
		t.Fatalf("rfc3339: %v %v", got, err)
	}
	got, err = ParseSince("2026-10-01")
	if err != nil || got.Format(time.RFC3339) != "2026-10-01T00:00:00Z" {
		t.Fatalf("date: %v %v", got, err)
	}
	if _, err := ParseSince("last tuesday"); err == nil {
		t.Fatal("want error for a non-time")
	}
	_, err = ParseSince("nope")
	te, ok := apperr.As(err)
	if !ok || te.Code != apperr.CodeBadRequest {
		t.Fatalf("want bad_request, got %v", err)
	}
}
