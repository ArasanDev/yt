package youtube

import (
	"regexp"
	"strings"

	"github.com/ArasanDev/yt/internal/apperr"
)

var (
	videoIDPattern = regexp.MustCompile(
		`(?:youtube\.com/(?:watch\?[^#\s]*v=|shorts/|live/|embed/|v/)|youtu\.be/)([\w-]{11})`)
	bareIDPattern = regexp.MustCompile(`^[\w-]{11}$`)
)

// OriginalLanguageValue is the sentinel an agent passes to ask for the video's
// own track with no machine translation.
const OriginalLanguageValue = "original"

// ExtractVideoID accepts a YouTube URL or a bare 11-character video id.
func ExtractVideoID(urlOrID string) (string, error) {
	s := strings.TrimSpace(urlOrID)
	if bareIDPattern.MatchString(s) {
		return s, nil
	}
	if m := videoIDPattern.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	te := apperr.New(apperr.CodeInvalidID, "could not extract a YouTube video id from %q", urlOrID)
	te.Suggestion = "Pass a watch/youtu.be/shorts/live/embed URL or a bare 11-character video ID."
	return "", te
}

// ParseLanguages parses a comma-separated language list and defaults to "en".
func ParseLanguages(csv string) []string {
	var out []string
	for _, s := range strings.Split(csv, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = []string{"en"}
	}
	return out
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
