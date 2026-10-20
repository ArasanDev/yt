package transcript

import (
	"fmt"
	"strings"
)

// Timestamp formats whole seconds as MM:SS, or HH:MM:SS past an hour.
func Timestamp(seconds float64) string {
	s := int(seconds)
	h, rem := s/3600, s%3600
	m, sec := rem/60, rem%60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

// TimestampSRT formats seconds as an SRT timestamp with millisecond precision.
func TimestampSRT(seconds float64) string {
	ms := int(seconds*1000 + 0.5)
	h, rem := ms/3600000, ms%3600000
	m, rem := rem/60000, rem%60000
	s, ms := rem/1000, rem%1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// TruncateRunes is rune-safe: byte slicing a description could split a
// multi-byte character. A limit of 0 means no truncation.
func TruncateRunes(s string, limit int) (string, bool) {
	if limit <= 0 {
		return s, false
	}
	r := []rune(s)
	if len(r) <= limit {
		return s, false
	}
	return string(r[:limit]) + "…", true
}

// WordCount counts whitespace-separated words across all cues.
func WordCount(segs []Segment) int {
	n := 0
	for _, s := range segs {
		n += len(strings.Fields(s.Text))
	}
	return n
}
