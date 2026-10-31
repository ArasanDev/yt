package youtube

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ArasanDev/yt/internal/apperr"
)

func responseWith(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestReadBodyEnforcesLimitAsTypedError(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		limit       int64
		header      http.Header
		wantLen     int
		wantErr     bool
		wantCode    apperr.Code
		wantContain string
		wantAfter   int
	}{
		{
			name:    "body exactly at limit is accepted",
			status:  http.StatusOK,
			body:    strings.Repeat("x", 100),
			limit:   100,
			wantLen: 100,
		},
		{
			name:        "body one byte over limit is too large",
			status:      http.StatusOK,
			body:        strings.Repeat("x", 101),
			limit:       100,
			wantErr:     true,
			wantCode:    apperr.CodeUpstreamChanged,
			wantContain: "response too large",
		},
		{
			name:        "truncated JSON is not mislabeled as a parse failure",
			status:      http.StatusOK,
			body:        strings.Repeat("x", 4096),
			limit:       100,
			wantErr:     true,
			wantCode:    apperr.CodeUpstreamChanged,
			wantContain: "100-byte limit",
		},
		{
			name:      "non-200 keeps the HTTP classification",
			status:    http.StatusTooManyRequests,
			body:      "",
			limit:     100,
			header:    http.Header{"Retry-After": []string{"5"}},
			wantErr:   true,
			wantCode:  apperr.CodeRateLimited,
			wantAfter: 5,
		},
		{
			name:     "non-200 not found",
			status:   http.StatusNotFound,
			body:     "",
			limit:    100,
			wantErr:  true,
			wantCode: apperr.CodeNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := readBody(responseWith(tt.status, tt.body, tt.header), tt.limit)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", body)
				}
				te, ok := apperr.As(err)
				if !ok {
					t.Fatalf("expected a typed error, got %T: %v", err, err)
				}
				if te.Code != tt.wantCode {
					t.Fatalf("code: got %q, want %q (%v)", te.Code, tt.wantCode, err)
				}
				if tt.wantContain != "" && !strings.Contains(te.Message, tt.wantContain) {
					t.Fatalf("message %q does not contain %q", te.Message, tt.wantContain)
				}
				if tt.wantAfter != 0 && te.RetryAfterSeconds != tt.wantAfter {
					t.Fatalf("retry_after_seconds: got %d, want %d", te.RetryAfterSeconds, tt.wantAfter)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(body) != tt.wantLen {
				t.Fatalf("body length: got %d, want %d", len(body), tt.wantLen)
			}
		})
	}
}

func TestPlayerResponseLiveness(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantNow bool
		wantWas bool
	}{
		{
			name:    "ordinary VOD",
			raw:     `{"videoDetails":{"isLiveContent":false}}`,
			wantNow: false,
			wantWas: false,
		},
		{
			name:    "historical live content only is not currently live",
			raw:     `{"videoDetails":{"isLiveContent":true}}`,
			wantNow: false,
			wantWas: true,
		},
		{
			name:    "videoDetails.isLive is current liveness",
			raw:     `{"videoDetails":{"isLiveContent":true,"isLive":true}}`,
			wantNow: true,
			wantWas: true,
		},
		{
			name:    "microformat isLiveNow is current liveness",
			raw:     `{"videoDetails":{"isLiveContent":true},"microformat":{"playerMicroformatRenderer":{"liveBroadcastDetails":{"isLiveNow":true}}}}`,
			wantNow: true,
			wantWas: true,
		},
		{
			name:    "ended broadcast carries liveBroadcastDetails but is not live",
			raw:     `{"videoDetails":{"isLiveContent":true},"microformat":{"playerMicroformatRenderer":{"liveBroadcastDetails":{"isLiveNow":false,"endTimestamp":"2026-01-01T00:00:00Z"}}}}`,
			wantNow: false,
			wantWas: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pr playerResponse
			if err := json.Unmarshal([]byte(tt.raw), &pr); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := pr.isLiveNow(); got != tt.wantNow {
				t.Errorf("isLiveNow: got %v, want %v", got, tt.wantNow)
			}
			if got := pr.isLive(); got != tt.wantNow {
				t.Errorf("isLive: got %v, want %v (current liveness)", got, tt.wantNow)
			}
			if got := pr.wasLive(); got != tt.wantWas {
				t.Errorf("wasLive: got %v, want %v", got, tt.wantWas)
			}
		})
	}
}
