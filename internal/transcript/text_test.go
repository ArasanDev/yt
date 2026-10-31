package transcript

import (
	"strings"
	"testing"
)

func TestTruncateRunes(t *testing.T) {
	s := "héllo wörld"
	got, truncated := TruncateRunes(s, 5)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.HasPrefix(got, "héllo") || !strings.HasSuffix(got, "…") {
		t.Errorf("rune-unsafe truncation: %q", got)
	}
	if full, tr := TruncateRunes(s, 0); tr || full != s {
		t.Errorf("limit 0 should mean no truncation, got %q truncated=%v", full, tr)
	}
	if _, tr := TruncateRunes(s, 100); tr {
		t.Error("short string should not be truncated")
	}
}
