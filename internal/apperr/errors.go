// Package apperr defines the typed error contract shared by the acquisition,
// CLI, and MCP layers. Agents branch on codes, not on prose strings.
package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Code is a stable machine-readable error code.
type Code string

const (
	CodeInvalidID       Code = "invalid_id"
	CodeBadRequest      Code = "bad_request"
	CodeNotFound        Code = "not_found"
	CodeChannelNotFound Code = "channel_not_found"
	CodeNotAChannel     Code = "not_a_channel"
	CodePrivate         Code = "private"
	CodeAgeRestricted   Code = "age_restricted"
	CodeBotCheck        Code = "bot_check"
	CodeNoCaptions      Code = "no_captions"
	CodeNoMatchingLang  Code = "no_matching_language"
	CodeRateLimited     Code = "rate_limited"
	CodeBlocked         Code = "blocked"
	CodeNetwork         Code = "network"
	CodeUpstreamChanged Code = "upstream_changed"
	CodeTimeout         Code = "timeout"
	CodeInternal        Code = "internal"
)

// Error is both a Go error and the machine-readable payload returned to agents
// in structuredContent when IsError is set.
type Error struct {
	Code               Code     `json:"code"`
	Message            string   `json:"message"`
	Retryable          bool     `json:"retryable"`
	RetryAfterSeconds  int      `json:"retry_after_seconds,omitempty"`
	AvailableLanguages []string `json:"available_languages,omitempty"`
	Suggestion         string   `json:"suggestion,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// New builds a typed error with a formatted message.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// As extracts the typed error from err, if any.
func As(err error) (*Error, bool) {
	var te *Error
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}

func parseRetryAfter(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return secs
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := int(time.Until(t).Seconds()); d > 0 {
			return d
		}
	}
	return 0
}

// ClassifyHTTPStatus maps an upstream HTTP status to the public error contract.
func ClassifyHTTPStatus(status int, retryAfter string) *Error {
	te := &Error{Code: CodeBlocked, Message: fmt.Sprintf("YouTube returned HTTP %d.", status)}
	switch {
	case status == http.StatusTooManyRequests:
		te.Code = CodeRateLimited
		te.Message = "YouTube rate-limited this request (HTTP 429)."
		te.Retryable = true
		te.RetryAfterSeconds = parseRetryAfter(retryAfter)
		te.Suggestion = "Wait before retrying; repeated calls from one IP are throttled."
	case status >= 500:
		te.Code = CodeNetwork
		te.Message = fmt.Sprintf("YouTube returned HTTP %d.", status)
		te.Retryable = true
	case status == http.StatusNotFound:
		te.Code = CodeNotFound
		te.Message = "YouTube returned HTTP 404; the video or caption URL was not found."
	case status == http.StatusForbidden:
		te.Code = CodeBlocked
		te.Message = "YouTube blocked the request (HTTP 403); the signed caption URL may have expired."
		te.Retryable = true
		te.Suggestion = "Retry; caption URLs are short-lived."
	}
	return te
}

// ClassifyPlayability maps YouTube's playabilityStatus to the error contract.
func ClassifyPlayability(status, reason string) *Error {
	reason = strings.TrimSpace(reason)
	upper := strings.ToUpper(strings.TrimSpace(status))
	lower := strings.ToLower(reason)
	switch upper {
	case "LOGIN_REQUIRED":
		switch {
		case strings.Contains(lower, "age"):
			te := New(CodeAgeRestricted, "This video is age-restricted and requires sign-in: %s", reason)
			te.Suggestion = "Age-restricted videos are not supported."
			return te
		case isBotCheckReason(lower):
			te := New(CodeBotCheck, "YouTube served a bot check (\"confirm you're not a bot\") instead of the video: %s", reason)
			te.Suggestion = "This is an IP-level block, not a problem with the video. Retry later or from a different network."
			return te
		default:
			te := New(CodePrivate, "This video is private or requires sign-in: %s", reason)
			te.Suggestion = "Private videos are not supported."
			return te
		}
	case "ERROR":
		if isNotFoundReason(lower) {
			return New(CodeNotFound, "YouTube reports this video as unavailable: %s", reason)
		}
		// A bare ERROR is often a transient or IP-level block, not a verdict on
		// the video, so it must not short-circuit the client cascade.
		te := New(CodeBlocked, "YouTube reported an error for this video: %s", reason)
		te.Suggestion = "This may be a temporary block; retry later or from another network."
		return te
	case "UNPLAYABLE":
		return New(CodeBlocked, "YouTube reports this video as unplayable: %s", reason)
	case "CONTENT_CHECK_REQUIRED":
		te := New(CodeAgeRestricted, "This video requires an age check: %s", reason)
		te.Suggestion = "Age-restricted videos are not supported."
		return te
	case "LIVE_STREAM_OFFLINE":
		return New(CodeNotFound, "This live stream is offline.")
	}
	return New(CodeBlocked, "YouTube playability status %s: %s", status, reason)
}

func isBotCheckReason(lowerReason string) bool {
	for _, s := range []string{"not a bot", "confirm you", "unusual traffic", "sign in to confirm", "bot check"} {
		if strings.Contains(lowerReason, s) {
			return true
		}
	}
	return false
}

func isNotFoundReason(lowerReason string) bool {
	for _, s := range []string{
		"not found", "does not exist", "no longer available",
		"has been removed", "unavailable", "private video", "deleted",
	} {
		if strings.Contains(lowerReason, s) {
			return true
		}
	}
	return false
}

// ClassifyContext turns deadline/cancellation errors into the public contract.
func ClassifyContext(err error) *Error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		te := New(CodeTimeout, "The request exceeded the time budget.")
		te.Retryable = true
		te.Suggestion = "Retry, or narrow the request with start_seconds/end_seconds."
		return te
	case errors.Is(err, context.Canceled):
		te := New(CodeTimeout, "The request was cancelled by the client.")
		te.Retryable = true
		return te
	}
	return nil
}

// Aggregate collapses the failure of the ANDROID -> IOS -> watch-page cascade
// into one typed error an agent can act on, preserving the most specific verdict
// and any Retry-After hint.
func Aggregate(errs []error) *Error {
	if len(errs) == 0 {
		return New(CodeUpstreamChanged, "all YouTube acquisition methods failed")
	}
	counts := map[Code]int{}
	retryAfter := 0
	for _, e := range errs {
		te, ok := As(e)
		if !ok {
			counts[CodeInternal]++
			continue
		}
		counts[te.Code]++
		if te.RetryAfterSeconds > retryAfter {
			retryAfter = te.RetryAfterSeconds
		}
	}
	if counts[CodeRateLimited] == len(errs) {
		te := New(CodeRateLimited, "All YouTube endpoints rate-limited the request.")
		te.Retryable = true
		te.RetryAfterSeconds = retryAfter
		te.Suggestion = "Wait before retrying; repeated calls from one IP are throttled."
		return te
	}
	if len(counts) == 1 {
		for code := range counts {
			te := New(code, "All YouTube acquisition methods failed with code %s.", code)
			te.Retryable = code == CodeRateLimited || code == CodeNetwork || code == CodeBlocked || code == CodeUpstreamChanged
			te.RetryAfterSeconds = retryAfter
			te.Suggestion = suggestionForCode(code)
			return te
		}
	}
	codes := make([]string, 0, len(counts))
	for code := range counts {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)
	te := New(CodeUpstreamChanged,
		"All YouTube acquisition methods failed (%s).", strings.Join(codes, ", "))
	te.Retryable = true
	te.RetryAfterSeconds = retryAfter
	te.Suggestion = "Retry once; if it persists, run `yt doctor` and update the client versions."
	return te
}

func suggestionForCode(code Code) string {
	switch code {
	case CodeBlocked:
		return "YouTube may be blocking this IP or the video is region/embed restricted. Retry later or from another network."
	case CodeNetwork:
		return "Transient network failure; retry."
	case CodeRateLimited:
		return "Wait before retrying."
	case CodeUpstreamChanged:
		return "Retry once; if it persists, run `yt doctor` and update the client versions."
	case CodeChannelNotFound:
		return "Check the handle or channel URL; run `yt channel @handle` with a channel that exists."
	case CodeNotAChannel:
		return "Pass a channel handle (@name), a channel URL, or a UC… channel id."
	}
	return ""
}
