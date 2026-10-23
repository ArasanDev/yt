package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yt/internal/apperr"
)

type fakeYouTube struct {
	server        *httptest.Server
	client        *Client
	playerCalls   int32
	json3Calls    int32
	srv3Calls     int32
	playability   string
	reason        string
	json3Status   int
	captionBase   string
	description   string
	isLiveNow     bool
	isLiveContent bool
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
			"videoDetails":{"videoId":"testvideo01","title":"T","author":"A","channelId":"C","lengthSeconds":"40","viewCount":"5","shortDescription":%q,"isLiveContent":%t,"isLive":%t},
			"microformat":{"playerMicroformatRenderer":{"liveBroadcastDetails":{"isLiveNow":%t}}},
			"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[
				{"baseUrl":%q,"languageCode":"en","kind":"asr","isTranslatable":true,"name":{"simpleText":"English"}}
			]}}
		}`, f.playability, f.reason, desc, f.isLiveContent, f.isLiveNow, f.isLiveNow, f.captionBase)
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

	cfg := DefaultConfig()
	cfg.InnertubeURL = srv.URL + "/youtubei/v1/player?prettyPrint=false"
	cfg.WatchPageURL = srv.URL + "/watch?v="
	f.client = NewClientWithConfig(cfg)
	return f, srv.Close
}

func TestPlayerIsFetchedOncePerVideo(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	c := f.client
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

	c := f.client
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

	c := f.client
	for i := 0; i < 2; i++ {
		_, _, err := c.Player(context.Background(), "testvideo01")
		te, ok := apperr.As(err)
		if !ok || te.Code != apperr.CodeBotCheck {
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

	_, _, err := f.client.Player(context.Background(), "testvideo01")
	te, ok := apperr.As(err)
	if !ok || te.Code != apperr.CodePrivate {
		t.Fatalf("want private, got %v", err)
	}
	if te.Suggestion == "" {
		t.Error("private error should carry a suggestion")
	}
}

func TestBotCheckIsNotReportedAsPrivate(t *testing.T) {
	te := apperr.ClassifyPlayability("LOGIN_REQUIRED", "Sign in to confirm you're not a bot")
	if te.Code != apperr.CodeBotCheck {
		t.Fatalf("bot check must not be classified as private, got %s", te.Code)
	}
	if te.Code == apperr.CodePrivate {
		t.Fatal("explicitly wrong")
	}
}

func TestDownloadSegmentsFallsBackWhenJSON3Fails(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	f.json3Status = http.StatusTooManyRequests

	segs, err := f.client.downloadSegments(context.Background(), f.captionBase, "", false)
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
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	tr, err := f.client.Transcript(context.Background(), "testvideo01",
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

	segs, err := f.client.downloadSegments(context.Background(), f.captionBase, "", false)
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
	te := apperr.Aggregate([]error{
		&apperr.Error{Code: apperr.CodeRateLimited, Retryable: true, RetryAfterSeconds: 7},
		&apperr.Error{Code: apperr.CodeRateLimited, Retryable: true},
	})
	if te.Code != apperr.CodeRateLimited {
		t.Fatalf("want rate_limited, got %s", te.Code)
	}
	if te.RetryAfterSeconds != 7 || !te.Retryable {
		t.Fatalf("retry hint lost: %+v", te)
	}
}

func TestInfoReportsCaptionsAndEstimate(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	info, err := f.client.Info(context.Background(), "testvideo01", 500)
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
	_, err := NewClient().requestWithRetry(ctx, http.MethodGet, srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if te, ok := apperr.As(err); !ok || te.Code != apperr.CodeTimeout {
		t.Fatalf("want timeout code, got %v", err)
	}
}

func TestPlayerPrefersPositiveCacheOverNegative(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	c := f.client
	c.memo.Put("neg:testvideo01", apperr.New(apperr.CodeBlocked, "stale block"), time.Minute, 32)
	c.memo.Put("player:testvideo01", playerCache{pr: &playerResponse{}, via: "innertube-android"}, time.Minute, 1024)

	_, via, err := c.Player(context.Background(), "testvideo01")
	if err != nil {
		t.Fatalf("positive cache entry should win, got %v", err)
	}
	if via != "innertube-android" {
		t.Fatalf("unexpected via %q", via)
	}
	if got := atomic.LoadInt32(&f.playerCalls); got != 0 {
		t.Fatalf("served from cache but made %d upstream calls", got)
	}
}

func TestPlayerDeduplicatesConcurrentFetches(t *testing.T) {
	const waiters = 8
	var calls int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{
			"playabilityStatus":{"status":"OK"},
			"videoDetails":{"videoId":"testvideo01","title":"T","lengthSeconds":"40"},
			"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[
				{"baseUrl":"http://example.invalid/c","languageCode":"en","kind":"asr"}
			]}}
		}`)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.InnertubeURL = srv.URL + "/youtubei/v1/player"
	cfg.WatchPageURL = srv.URL + "/watch?v="
	c := NewClientWithConfig(cfg)

	start := make(chan struct{})
	ready := sync.WaitGroup{}
	ready.Add(waiters)
	errs := make(chan error, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			_, _, err := c.Player(context.Background(), "testvideo01")
			errs <- err
		}()
	}
	ready.Wait()
	close(start)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no player request reached the server")
	}
	// Give the remaining callers time to join the in-flight call.
	time.Sleep(100 * time.Millisecond)
	releaseAll()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Player: %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("want 1 upstream fetch for %d concurrent callers, got %d", waiters, got)
	}
}

func TestTranscriptRefreshesPlayerAfterCaptionURLFailure(t *testing.T) {
	var playerCalls, staleCalls, freshCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/youtubei/v1/player", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&playerCalls, 1)
		base := "http://" + r.Host + "/captions/stale?x=1&fmt=srv3"
		if n > 1 {
			base = "http://" + r.Host + "/captions/fresh?x=1&fmt=srv3"
		}
		fmt.Fprintf(w, `{
			"playabilityStatus":{"status":"OK"},
			"videoDetails":{"videoId":"testvideo01","title":"T","lengthSeconds":"40"},
			"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[
				{"baseUrl":%q,"languageCode":"en","kind":"asr","isTranslatable":true}
			]}}
		}`, base)
	})
	// The first player response carries a signed URL that is already rejected;
	// both formats fail with 403 so the caller re-signs the URL.
	mux.HandleFunc("/captions/stale", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&staleCalls, 1)
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/captions/fresh", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&freshCalls, 1)
		fmt.Fprint(w, `{"events":[{"tStartMs":0,"dDurationMs":1000,"segs":[{"utf8":"fresh transcript"}]}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.InnertubeURL = srv.URL + "/youtubei/v1/player"
	cfg.WatchPageURL = srv.URL + "/watch?v="
	c := NewClientWithConfig(cfg)

	tr, err := c.Transcript(context.Background(), "testvideo01",
		TranscriptOptions{Languages: []string{"en"}, AllowTranslation: true})
	if err != nil {
		t.Fatalf("expected the refreshed caption URL to succeed, got %v", err)
	}
	if len(tr.Segments) != 1 || tr.Segments[0].Text != "fresh transcript" {
		t.Fatalf("unexpected transcript: %+v", tr.Segments)
	}
	if got := atomic.LoadInt32(&playerCalls); got != 2 {
		t.Errorf("want 2 player fetches (initial + refresh), got %d", got)
	}
	if got := atomic.LoadInt32(&staleCalls); got != 2 {
		t.Errorf("want both stale formats attempted, got %d", got)
	}
	if got := atomic.LoadInt32(&freshCalls); got != 1 {
		t.Errorf("want 1 fresh caption request, got %d", got)
	}
}

func TestRateLimitStopsClientCascade(t *testing.T) {
	var mu sync.Mutex
	uas := map[string]int{}
	playerHits, watchHits := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if strings.HasPrefix(r.URL.Path, "/watch") {
			watchHits++
		} else {
			playerHits++
			uas[r.Header.Get("User-Agent")]++
		}
		mu.Unlock()
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.InnertubeURL = srv.URL + "/youtubei/v1/player"
	cfg.WatchPageURL = srv.URL + "/watch?v="
	c := NewClientWithConfig(cfg)

	_, _, err := c.Player(context.Background(), "testvideo01")
	te, ok := apperr.As(err)
	if !ok || te.Code != apperr.CodeRateLimited {
		t.Fatalf("want rate_limited, got %v", err)
	}
	if te.RetryAfterSeconds != 1 {
		t.Fatalf("retry_after_seconds lost: %+v", te)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(uas) != 1 {
		t.Fatalf("429 must stop the client cascade, got UAs %v", uas)
	}
	if _, ok := uas[clientContexts[0].UA]; !ok {
		t.Fatalf("expected only the ANDROID client to be tried, got %v", uas)
	}
	if watchHits != 0 {
		t.Fatalf("watch page must not be scraped after a 429, got %d hits", watchHits)
	}
	if playerHits > maxAttempts {
		t.Fatalf("unexpected number of player requests: %d", playerHits)
	}
}

func TestTranscriptCachingUsesCurrentNotHistoricalLiveness(t *testing.T) {
	opts := TranscriptOptions{Languages: []string{"en"}, AllowTranslation: true}

	t.Run("live now is never cached", func(t *testing.T) {
		f, cleanup := newFakeYouTube(t)
		defer cleanup()
		f.isLiveNow = true
		f.isLiveContent = true

		for i := 0; i < 2; i++ {
			if _, err := f.client.Transcript(context.Background(), "testvideo01", opts); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		if got := atomic.LoadInt32(&f.json3Calls); got != 2 {
			t.Fatalf("live transcript must not be cached; got %d downloads", got)
		}
	})

	t.Run("finished stream is cached", func(t *testing.T) {
		f, cleanup := newFakeYouTube(t)
		defer cleanup()
		f.isLiveContent = true
		f.isLiveNow = false

		for i := 0; i < 2; i++ {
			if _, err := f.client.Transcript(context.Background(), "testvideo01", opts); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		if got := atomic.LoadInt32(&f.json3Calls); got != 1 {
			t.Fatalf("ended stream transcript should be cached; got %d downloads", got)
		}
	})
}

func TestInfoSeparatesCurrentAndHistoricalLiveness(t *testing.T) {
	t.Run("live now", func(t *testing.T) {
		f, cleanup := newFakeYouTube(t)
		defer cleanup()
		f.isLiveNow = true
		f.isLiveContent = true

		info, err := f.client.Info(context.Background(), "testvideo01", 500)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsLive || !info.IsLiveContent {
			t.Fatalf("want live + live content, got %+v", info)
		}
	})

	t.Run("finished stream", func(t *testing.T) {
		f, cleanup := newFakeYouTube(t)
		defer cleanup()
		f.isLiveNow = false
		f.isLiveContent = true

		info, err := f.client.Info(context.Background(), "testvideo01", 500)
		if err != nil {
			t.Fatal(err)
		}
		if info.IsLive {
			t.Fatal("a finished stream must not be reported as currently live")
		}
		if !info.IsLiveContent {
			t.Fatal("historical liveness should still be reported")
		}
	})
}
