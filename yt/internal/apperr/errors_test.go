package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestAggregateSharedAndMixedCodes(t *testing.T) {
	blockedShort := New(CodeBlocked, "blocked")
	blockedDetailed := &Error{
		Code:              CodeBlocked,
		Message:           "YouTube blocked the request (HTTP 403); the signed caption URL may have expired.",
		Retryable:         true,
		RetryAfterSeconds: 4,
		Suggestion:        "Retry; caption URLs are short-lived.",
	}
	networkDetailed := &Error{
		Code:              CodeNetwork,
		Message:           "network error: dial tcp: connection reset",
		Retryable:         true,
		RetryAfterSeconds: 9,
	}
	timeout := &Error{
		Code:       CodeTimeout,
		Message:    "The request exceeded the time budget.",
		Retryable:  true,
		Suggestion: "Retry, or narrow the request with start_seconds/end_seconds.",
	}
	noLangEN := &Error{
		Code:               CodeNoMatchingLang,
		Message:            "No caption track matches requested languages fr.",
		AvailableLanguages: []string{"en", "de"},
		Suggestion:         "Retry with one of available_languages.",
	}
	noLangES := &Error{
		Code:               CodeNoMatchingLang,
		Message:            "No caption track matches requested languages fr.",
		AvailableLanguages: []string{"es", "en"},
	}

	tests := []struct {
		name        string
		errs        []error
		wantCode    Code
		wantRetry   bool
		wantAfter   int
		wantMessage string // exact when set
		wantContain string // substring when exact is not set
		wantSuggest string // exact when set
		wantLangs   []string
	}{
		{
			name:        "shared blocked keeps most specific message and suggestion",
			errs:        []error{blockedShort, blockedDetailed},
			wantCode:    CodeBlocked,
			wantRetry:   true,
			wantAfter:   4,
			wantMessage: blockedDetailed.Message,
			wantSuggest: blockedDetailed.Suggestion,
		},
		{
			name:        "shared timeout is retryable",
			errs:        []error{timeout, &Error{Code: CodeTimeout, Message: "The request exceeded the time budget."}},
			wantCode:    CodeTimeout,
			wantRetry:   true,
			wantAfter:   0,
			wantMessage: timeout.Message,
			wantSuggest: timeout.Suggestion,
		},
		{
			name: "shared rate limit preserves retry-after and message",
			errs: []error{
				&Error{Code: CodeRateLimited, Message: "YouTube rate-limited this request (HTTP 429).", Retryable: true, RetryAfterSeconds: 7},
				&Error{Code: CodeRateLimited, Message: "YouTube rate-limited this request (HTTP 429).", Retryable: true, RetryAfterSeconds: 2},
			},
			wantCode:    CodeRateLimited,
			wantRetry:   true,
			wantAfter:   7,
			wantMessage: "YouTube rate-limited this request (HTTP 429).",
			wantSuggest: "Wait before retrying.",
		},
		{
			name:        "shared no_matching_language unions available languages",
			errs:        []error{noLangEN, noLangES},
			wantCode:    CodeNoMatchingLang,
			wantRetry:   false,
			wantAfter:   0,
			wantMessage: noLangEN.Message,
			wantSuggest: noLangEN.Suggestion,
			wantLangs:   []string{"en", "de", "es"},
		},
		{
			name:        "shared code with empty messages falls back to generic message",
			errs:        []error{&Error{Code: CodeBlocked}, &Error{Code: CodeBlocked}},
			wantCode:    CodeBlocked,
			wantRetry:   true,
			wantAfter:   0,
			wantMessage: "All YouTube acquisition methods failed with code blocked.",
			wantSuggest: suggestionForCode(CodeBlocked),
		},
		{
			name:        "mixed codes collapse to upstream_changed with hints",
			errs:        []error{blockedDetailed, networkDetailed, &Error{Code: CodeNetwork, Message: "network error: i/o timeout", AvailableLanguages: []string{"pt"}}},
			wantCode:    CodeUpstreamChanged,
			wantRetry:   true,
			wantAfter:   9,
			wantContain: "All YouTube acquisition methods failed (blocked, network).",
			wantSuggest: suggestionForCode(CodeUpstreamChanged),
			wantLangs:   []string{"pt"},
		},
		{
			name:        "mixed non-retryable codes are not marked retryable",
			errs:        []error{New(CodePrivate, "private"), New(CodeNotFound, "gone")},
			wantCode:    CodeUpstreamChanged,
			wantRetry:   false,
			wantAfter:   0,
			wantContain: "(not_found, private)",
		},
		{
			name:        "untyped error is grouped as internal",
			errs:        []error{errors.New("boom")},
			wantCode:    CodeInternal,
			wantRetry:   false,
			wantAfter:   0,
			wantMessage: "boom",
		},
		{
			name:        "wrapped typed error is unwrapped",
			errs:        []error{fmt.Errorf("android attempt: %w", blockedDetailed)},
			wantCode:    CodeBlocked,
			wantRetry:   true,
			wantAfter:   4,
			wantMessage: blockedDetailed.Message,
			wantSuggest: blockedDetailed.Suggestion,
		},
		{
			name:        "nil entries are ignored",
			errs:        []error{nil, blockedDetailed, nil},
			wantCode:    CodeBlocked,
			wantRetry:   true,
			wantAfter:   4,
			wantMessage: blockedDetailed.Message,
			wantSuggest: blockedDetailed.Suggestion,
		},
		{
			name:        "empty slice returns retryable upstream_changed",
			errs:        nil,
			wantCode:    CodeUpstreamChanged,
			wantRetry:   true,
			wantAfter:   0,
			wantMessage: "All YouTube acquisition methods failed.",
			wantSuggest: suggestionForCode(CodeUpstreamChanged),
		},
		{
			name:        "context timeout classified error is retryable",
			errs:        []error{ClassifyContext(context.DeadlineExceeded), ClassifyContext(context.DeadlineExceeded)},
			wantCode:    CodeTimeout,
			wantRetry:   true,
			wantAfter:   0,
			wantMessage: "The request exceeded the time budget.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Aggregate(tt.errs)
			if got.Code != tt.wantCode {
				t.Fatalf("code: got %q, want %q (err=%+v)", got.Code, tt.wantCode, got)
			}
			if got.Retryable != tt.wantRetry {
				t.Errorf("retryable: got %v, want %v", got.Retryable, tt.wantRetry)
			}
			if got.RetryAfterSeconds != tt.wantAfter {
				t.Errorf("retry_after_seconds: got %d, want %d", got.RetryAfterSeconds, tt.wantAfter)
			}
			if tt.wantMessage != "" && got.Message != tt.wantMessage {
				t.Errorf("message: got %q, want %q", got.Message, tt.wantMessage)
			}
			if tt.wantContain != "" && !strings.Contains(got.Message, tt.wantContain) {
				t.Errorf("message %q does not contain %q", got.Message, tt.wantContain)
			}
			if tt.wantSuggest != "" && got.Suggestion != tt.wantSuggest {
				t.Errorf("suggestion: got %q, want %q", got.Suggestion, tt.wantSuggest)
			}
			if !reflect.DeepEqual(got.AvailableLanguages, tt.wantLangs) {
				t.Errorf("available_languages: got %v, want %v", got.AvailableLanguages, tt.wantLangs)
			}
		})
	}
}

func TestAggregateDoesNotMutateInputs(t *testing.T) {
	in := &Error{Code: CodeBlocked, Message: "blocked A", RetryAfterSeconds: 3}
	_ = Aggregate([]error{in, &Error{Code: CodeBlocked, Message: "blocked A and more"}})
	if in.Message != "blocked A" || in.RetryAfterSeconds != 3 {
		t.Fatalf("input error mutated: %+v", in)
	}
}

func TestAggregateMixedNonRetryableHasSuggestion(t *testing.T) {
	te := Aggregate([]error{
		New(CodePrivate, "private video"),
		New(CodeNotFound, "video not found"),
	})
	if te.Retryable {
		t.Fatalf("mixed verdict errors should not be retryable: %+v", te)
	}
	if te.Suggestion == "" {
		t.Fatal("mixed non-retryable errors should still carry a suggestion")
	}
}

func TestClassifyHTTPStatusRetryAfter(t *testing.T) {
	te := ClassifyHTTPStatus(429, "12")
	if te.Code != CodeRateLimited || te.RetryAfterSeconds != 12 || !te.Retryable {
		t.Fatalf("unexpected classification: %+v", te)
	}
}

func TestClassifyHTTPStatusTable(t *testing.T) {
	tests := []struct {
		status int
		code   Code
		retry  bool
		after  int
	}{
		{http.StatusForbidden, CodeBlocked, true, 0},
		{http.StatusNotFound, CodeNotFound, false, 0},
		{http.StatusTooManyRequests, CodeRateLimited, true, 0},
		{http.StatusInternalServerError, CodeNetwork, true, 0},
		{http.StatusBadRequest, CodeBlocked, false, 0},
	}
	for _, tt := range tests {
		te := ClassifyHTTPStatus(tt.status, "")
		if te.Code != tt.code || te.Retryable != tt.retry || te.RetryAfterSeconds != tt.after {
			t.Errorf("status %d: got %+v, want code=%s retry=%v after=%d", tt.status, te, tt.code, tt.retry, tt.after)
		}
	}
}

func TestClassifyPlayabilityPrivateIsNotRetryable(t *testing.T) {
	te := ClassifyPlayability("ERROR", "This video is private")
	if te.Code != CodePrivate || te.Retryable {
		t.Fatalf("private error should be non-retryable, got %+v", te)
	}
}

func TestClassifyPlayabilityBotCheck(t *testing.T) {
	te := ClassifyPlayability("LOGIN_REQUIRED", "Sign in to confirm you're not a bot")
	if te.Code != CodeBotCheck || te.Retryable {
		t.Fatalf("bot check should be non-retryable, got %+v", te)
	}
}
