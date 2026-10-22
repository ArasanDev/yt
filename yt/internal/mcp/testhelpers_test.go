package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yt/internal/youtube"
)

type fakeYouTube struct {
	server      *httptest.Server
	client      *youtube.Client
	playerCalls int32
	json3Calls  int32
	srv3Calls   int32
	playability string
	reason      string
	json3Status int
	captionBase string
	description string
}

func newFakeYouTube(t *testing.T) (*fakeYouTube, func()) {
	t.Helper()
	f := &fakeYouTube{playability: "OK", json3Status: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/youtubei/v1/player", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.playerCalls, 1)
		desc := f.description
		if desc == "" {
			desc = "desc"
		}
		fmt.Fprintf(w, `{
			"playabilityStatus":{"status":%q,"reason":%q},
			"videoDetails":{"videoId":"testvideo01","title":"T","author":"A","channelId":"C","lengthSeconds":"40","viewCount":"5","shortDescription":%q,"isLiveContent":false},
			"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[
				{"baseUrl":%q,"languageCode":"en","kind":"asr","isTranslatable":true,"name":{"simpleText":"English"}}
			]}}
		}`, f.playability, f.reason, desc, f.captionBase)
	})
	mux.HandleFunc("/captions", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "fmt=srv3") {
			atomic.AddInt32(&f.srv3Calls, 1)
			fmt.Fprint(w, `<transcript><body><p t="0" d="1000"><s>Hello</s><s>world</s></p></body></transcript>`)
			return
		}
		atomic.AddInt32(&f.json3Calls, 1)
		if f.json3Status != http.StatusOK {
			w.WriteHeader(f.json3Status)
			return
		}
		fmt.Fprint(w, `{"events":[{"tStartMs":0,"dDurationMs":1000,"segs":[{"utf8":"Hello world"}]}`)
		for i := 1; i < 10; i++ {
			fmt.Fprintf(w, `,{"tStartMs":%d,"dDurationMs":1000,"segs":[{"utf8":"line %d of speech"}]}`, i*1000, i)
		}
		fmt.Fprint(w, `]}`)
	})
	srv := httptest.NewServer(mux)
	f.server = srv
	f.captionBase = srv.URL + "/captions?x=1&fmt=srv3"

	cfg := youtube.DefaultConfig()
	cfg.InnertubeURL = srv.URL + "/youtubei/v1/player?prettyPrint=false"
	cfg.WatchPageURL = srv.URL + "/watch?v="
	f.client = youtube.NewClientWithConfig(cfg)
	return f, srv.Close
}

type fakeChannelServer struct {
	server       *httptest.Server
	client       *youtube.Client
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
	cfg := youtube.DefaultConfig()
	cfg.ResolveURL = srv.URL + "/resolve"
	cfg.FeedURL = srv.URL + "/feed?channel_id="
	f.client = youtube.NewClientWithConfig(cfg)
	return f, srv.Close
}

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
