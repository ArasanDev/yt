// Typed tool errors: agents branch on codes, not on prose strings.
package main

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

type ErrorCode string

const (
	CodeInvalidID       ErrorCode = "invalid_id"
	CodeBadRequest      ErrorCode = "bad_request"
	CodeNotFound        ErrorCode = "not_found"
	CodeChannelNotFound ErrorCode = "channel_not_found"
	CodeNotAChannel     ErrorCode = "not_a_channel"
	CodePrivate         ErrorCode = "private"
	CodeAgeRestricted   ErrorCode = "age_restricted"
	CodeBotCheck        ErrorCode = "bot_check"
	CodeNoCaptions      ErrorCode = "no_captions"
	CodeNoMatchingLang  ErrorCode = "no_matching_language"
	CodeRateLimited     ErrorCode = "rate_limited"
	CodeBlocked         ErrorCode = "blocked"
	CodeNetwork         ErrorCode = "network"
	CodeUpstreamChanged ErrorCode = "upstream_changed"
	CodeTimeout         ErrorCode = "timeout"
	CodeInternal        ErrorCode = "internal"
)

// ToolError is both a Go error and the machine-readable payload returned to
// agents in structuredContent when IsError is set.
type ToolError struct {
	Code               ErrorCode `json:"code"`
	Message            string    `json:"message"`
	Retryable          bool      `json:"retryable"`
	RetryAfterSeconds  int       `json:"retry_after_seconds,omitempty"`
	AvailableLanguages []string  `json:"available_languages,omitempty"`
	Suggestion         string    `json:"suggestion,omitempty"`
}

func (e *ToolError) Error() string { return e.Message }

func newToolError(code ErrorCode, format string, args ...any) *ToolError {
	return &ToolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func toolErrorOf(err error) (*ToolError, bool) {
	var te *ToolError
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

func classifyHTTPStatus(status int, retryAfter string) *ToolError {
	te := &ToolError{Code: CodeBlocked, Message: fmt.Sprintf("YouTube returned HTTP %d.", status)}
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

func classifyPlayability(status, reason string) *ToolError {
	reason = strings.TrimSpace(reason)
	upper := strings.ToUpper(strings.TrimSpace(status))
	lower := strings.ToLower(reason)
	switch upper {
	case "LOGIN_REQUIRED":
		switch {
		case strings.Contains(lower, "age"):
			te := newToolError(CodeAgeRestricted, "This video is age-restricted and requires sign-in: %s", reason)
			te.Suggestion = "Age-restricted videos are not supported."
			return te
		case isBotCheckReason(lower):
			te := newToolError(CodeBotCheck, "YouTube served a bot check (\"confirm you're not a bot\") instead of the video: %s", reason)
			te.Suggestion = "This is an IP-level block, not a problem with the video. Retry later or from a different network."
			return te
		default:
			te := newToolError(CodePrivate, "This video is private or requires sign-in: %s", reason)
			te.Suggestion = "Private videos are not supported."
			return te
		}
	case "ERROR":
		if isNotFoundReason(lower) {
			return newToolError(CodeNotFound, "YouTube reports this video as unavailable: %s", reason)
		}
		// A bare ERROR is often a transient or IP-level block, not a verdict on
		// the video, so it must not short-circuit the client cascade.
		te := newToolError(CodeBlocked, "YouTube reported an error for this video: %s", reason)
		te.Suggestion = "This may be a temporary block; retry later or from another network."
		return te
	case "UNPLAYABLE":
		return newToolError(CodeBlocked, "YouTube reports this video as unplayable: %s", reason)
	case "CONTENT_CHECK_REQUIRED":
		te := newToolError(CodeAgeRestricted, "This video requires an age check: %s", reason)
		te.Suggestion = "Age-restricted videos are not supported."
		return te
	case "LIVE_STREAM_OFFLINE":
		return newToolError(CodeNotFound, "This live stream is offline.")
	}
	return newToolError(CodeBlocked, "YouTube playability status %s: %s", status, reason)
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

func classifyContext(err error) *ToolError {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		te := newToolError(CodeTimeout, "The request exceeded the time budget.")
		te.Retryable = true
		te.Suggestion = "Retry, or narrow the request with start_seconds/end_seconds."
		return te
	case errors.Is(err, context.Canceled):
		te := newToolError(CodeTimeout, "The request was cancelled by the client.")
		te.Retryable = true
		return te
	}
	return nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// aggregateToolErrors collapses the failure of the ANDROID -> IOS -> watch-page
// cascade into one typed error an agent can act on, preserving the most
// specific verdict and any Retry-After hint.
func aggregateToolErrors(errs []error) *ToolError {
	if len(errs) == 0 {
		return newToolError(CodeUpstreamChanged, "all YouTube acquisition methods failed")
	}
	counts := map[ErrorCode]int{}
	retryAfter := 0
	for _, e := range errs {
		te, ok := toolErrorOf(e)
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
		te := newToolError(CodeRateLimited, "All YouTube endpoints rate-limited the request.")
		te.Retryable = true
		te.RetryAfterSeconds = retryAfter
		te.Suggestion = "Wait before retrying; repeated calls from one IP are throttled."
		return te
	}
	if len(counts) == 1 {
		for code := range counts {
			te := newToolError(code, "All YouTube acquisition methods failed with code %s.", code)
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
	te := newToolError(CodeUpstreamChanged,
		"All YouTube acquisition methods failed (%s).", strings.Join(codes, ", "))
	te.Retryable = true
	te.RetryAfterSeconds = retryAfter
	te.Suggestion = "Retry once; if it persists, run `yt doctor` and update the client versions."
	return te
}

func suggestionForCode(code ErrorCode) string {
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
