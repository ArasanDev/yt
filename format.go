// Output formatters: plain text, SRT, JSON.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func tsShort(seconds float64) string {
	s := int(seconds)
	h, rem := s/3600, s%3600
	m, sec := rem/60, rem%60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

func tsSRT(seconds float64) string {
	ms := int(seconds*1000 + 0.5)
	h, rem := ms/3600000, ms%3600000
	m, rem := rem/60000, rem%60000
	s, ms := rem/1000, rem%1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

func formatSRT(t *Transcript) string {
	var b strings.Builder
	for i, s := range t.Segments {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, tsSRT(s.Start), tsSRT(s.Start+s.Duration), s.Text)
	}
	return b.String()
}

func formatJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
