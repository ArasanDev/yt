package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeYouTube struct {
	server      *httptest.Server
	playerCalls int32
	json3Calls  int32
	srv3Calls   int32
	playability string
	reason      string
	json3Status int
	captionBase string
	description string
}

// newFakeYouTube points the package's URL globals at a test server serving an
// innertube player response plus caption endpoints.
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

	oldInnertube, oldWatch := innertubeURL, watchPageURL
	innertubeURL = srv.URL + "/youtubei/v1/player?prettyPrint=false"
	watchPageURL = srv.URL + "/watch?v="
	return f, func() {
		innertubeURL, watchPageURL = oldInnertube, oldWatch
		srv.Close()
	}
}

func TestPlayerIsFetchedOncePerVideo(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	c := NewClient()
	for i := 0; i < 3; i++ {
		if _, _, err := c.Player(context.Background(), "testvideo01"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&f.playerCalls); got != 1 {
		t.Fatalf("want 1 player fetch (info+tracks+transcript share it), got %d", got)
	}
}

func TestTranscriptIsDownloadedOnce(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	c := NewClient()
	opts := TranscriptOptions{Languages: []string{"en"}, AllowTranslation: true}
	for i := 0; i < 2; i++ {
		if _, err := c.Transcript(context.Background(), "testvideo01", opts); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&f.json3Calls); got != 1 {
		t.Fatalf("want 1 caption download, got %d", got)
	}
}

func TestNegativeCacheForBotCheck(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	f.playability = "LOGIN_REQUIRED"
	f.reason = "Sign in to confirm you're not a bot"

	c := NewClient()
	for i := 0; i < 2; i++ {
		_, _, err := c.Player(context.Background(), "testvideo01")
		te, ok := toolErrorOf(err)
		if !ok || te.Code != CodeBotCheck {
			t.Fatalf("want bot_check, got %v", err)
		}
	}
	if got := atomic.LoadInt32(&f.playerCalls); got != 1 {
		t.Fatalf("negative result should be cached; got %d fetches", got)
	}
}

func TestPrivateVideoClassification(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	f.playability = "LOGIN_REQUIRED"
	f.reason = "This video is private"

	_, _, err := NewClient().Player(context.Background(), "testvideo01")
	te, ok := toolErrorOf(err)
	if !ok || te.Code != CodePrivate {
		t.Fatalf("want private, got %v", err)
	}
	if te.Suggestion == "" {
		t.Error("private error should carry a suggestion")
	}
}

func TestBotCheckIsNotReportedAsPrivate(t *testing.T) {
	te := classifyPlayability("LOGIN_REQUIRED", "Sign in to confirm you're not a bot")
	if te.Code != CodeBotCheck {
		t.Fatalf("bot check must not be classified as private, got %s", te.Code)
	}
	if te.Code == CodePrivate {
		t.Fatal("explicitly wrong")
	}
}

func TestDownloadSegmentsFallsBackWhenJSON3Fails(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	f.json3Status = http.StatusTooManyRequests

	segs, err := downloadSegments(context.Background(), f.captionBase, "", false)
	if err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	if len(segs) != 1 || segs[0].Text != "Hello world" {
		t.Fatalf("unexpected segments: %+v", segs)
	}
	if atomic.LoadInt32(&f.srv3Calls) != 1 {
		t.Errorf("srv3 fallback was not exercised")
	}
}

func TestTranscriptSourceIsSet(t *testing.T) {
	_, cleanup := newFakeYouTube(t)
	defer cleanup()

	tr, err := NewClient().Transcript(context.Background(), "testvideo01",
		TranscriptOptions{Languages: []string{"en"}, AllowTranslation: true})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Source != "innertube-android" {
		t.Errorf("provenance missing: source=%q", tr.Source)
	}
}

func TestJSON3IsUsedWhenBaseURLHasSrv3(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	segs, err := downloadSegments(context.Background(), f.captionBase, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 10 {
		t.Fatalf("want 10 json3 cues, got %d", len(segs))
	}
	if got := atomic.LoadInt32(&f.json3Calls); got != 1 {
		t.Errorf("want exactly 1 json3 request, got %d", got)
	}
	if got := atomic.LoadInt32(&f.srv3Calls); got != 0 {
		t.Errorf("srv3 fallback should not run when json3 works, got %d", got)
	}
}

func TestRateLimitedAggregationPreservesRetryAfter(t *testing.T) {
	te := aggregateToolErrors([]error{
		&ToolError{Code: CodeRateLimited, Retryable: true, RetryAfterSeconds: 7},
		&ToolError{Code: CodeRateLimited, Retryable: true},
	})
	if te.Code != CodeRateLimited {
		t.Fatalf("want rate_limited, got %s", te.Code)
	}
	if te.RetryAfterSeconds != 7 || !te.Retryable {
		t.Fatalf("retry hint lost: %+v", te)
	}
}

func TestInfoReportsCaptionsAndEstimate(t *testing.T) {
	_, cleanup := newFakeYouTube(t)
	defer cleanup()

	info, err := NewClient().Info(context.Background(), "testvideo01", 500)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Captions.HasCaptions || len(info.Captions.Auto) != 1 {
		t.Fatalf("caption summary wrong: %+v", info.Captions)
	}
	if info.EstimatedTranscriptChars != 40*17 {
		t.Errorf("want estimate 680, got %d", info.EstimatedTranscriptChars)
	}
}

func TestRequestRetryRespectsCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := requestWithRetry(ctx, http.MethodGet, srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if te, ok := toolErrorOf(err); !ok || te.Code != CodeTimeout {
		t.Fatalf("want timeout code, got %v", err)
	}
}
