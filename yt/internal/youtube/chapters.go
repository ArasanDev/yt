// Chapters are authored structure that already travels inside the description.
// Parsing them costs one regex pass and turns a 50-minute talk into addressable
// sections, which is what reading and citation plans actually need.
package youtube

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"yt/internal/transcript"
)

// Chapter is an author-supplied section marker.
type Chapter struct {
	StartSeconds int    `json:"start_seconds"`
	Timestamp    string `json:"timestamp"`
	Title        string `json:"title"`
}

var (
	// Headings arrive decorated in the wild: emoji ("📝 Chapters"), markdown
	// ("## Chapters"), bold ("**Chapters**"), bullets, and the inline form
	// ("Chapters: 00:00 Intro") that carries the first entry on the heading
	// line itself. Leading decoration is anything before the first letter or
	// digit; trailing decoration is anything non-alphanumeric after it, with
	// ':' preserved so the inline form keeps its colon.
	chapterHeadingDecor = regexp.MustCompile(`^[^\p{L}\p{N}]*`)
	// "Chapters", "Chapter", "Timestamps", "TIMESTAMPS:", optionally decorated.
	chapterHeadingPattern = regexp.MustCompile(`(?i)^(chapters?|timestamps?)\s*:?\s*$`)
	// Inline form: the heading word followed by a first timestamp entry,
	// with or without a colon ("Chapters: 00:00 Intro", "Chapters 0:00 Intro").
	chapterHeadingInlinePattern = regexp.MustCompile(`(?i)^(chapters?|timestamps?)\s*[:\s]\s*`)
	// Accepts "00:00 Title", "00:00 - Title", "0:00:00 Title", "* 00:00 Title".
	chapterLinePattern = regexp.MustCompile(`^\s*[-*•]?\s*(\d{1,2}):(\d{2})(?::(\d{2}))?\s*[-–—:)]?\s*(.+?)\s*$`)
)

// chapterHeadingRest reports whether the line is a chapter heading and returns
// the remainder of the line, which may hold the first timestamp entry.
func chapterHeadingRest(line string) (string, bool) {
	s := chapterHeadingDecor.ReplaceAllString(strings.TrimSpace(line), "")
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ':'
	})
	if chapterHeadingPattern.MatchString(s) {
		return "", true
	}
	if m := chapterHeadingInlinePattern.FindStringSubmatchIndex(s); m != nil {
		rest := strings.TrimSpace(s[m[1]:])
		if chapterLinePattern.MatchString(rest) {
			return rest, true
		}
	}
	return "", false
}

// parseChapters extracts a chapter list from a video description. Entries that
// are invalid (impossible clock values), duplicate, out of order, or past the
// video's end are dropped instead of poisoning the list; what remains must
// still look trustworthy: at least two strictly increasing timestamps that fit
// inside the video.
func parseChapters(desc string, durationSeconds int) []Chapter {
	lines := strings.Split(desc, "\n")
	start := -1
	var out []Chapter
	for i, line := range lines {
		rest, ok := chapterHeadingRest(line)
		if !ok {
			continue
		}
		start = i + 1
		// The heading line may carry the first entry inline:
		// "Chapters: 00:00 Intro".
		if m := chapterLinePattern.FindStringSubmatch(rest); m != nil {
			out = appendChapter(out, m, durationSeconds)
		}
		break
	}
	if start < 0 {
		return nil
	}

	for _, line := range lines[start:] {
		if strings.TrimSpace(line) == "" {
			// Blank lines are allowed inside a list; chapters are sometimes
			// separated by one. Prose after the list still ends it below.
			continue
		}
		m := chapterLinePattern.FindStringSubmatch(line)
		if m == nil {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = appendChapter(out, m, durationSeconds)
		if len(out) >= 200 {
			break
		}
	}

	if len(out) < 2 {
		return nil
	}
	return out
}

// appendChapter keeps an entry only when it is valid: plausible clock values,
// strictly after the previous kept entry, and — when the duration is known —
// starting inside the video. Duplicates, regressions, and stale overshoots are
// dropped; one bad entry no longer rejects the whole list.
func appendChapter(out []Chapter, m []string, durationSeconds int) []Chapter {
	title := strings.TrimSpace(m[4])
	if title == "" {
		return out
	}
	sec, ok := chapterSeconds(m[1], m[2], m[3])
	if !ok {
		return out
	}
	// An entry that starts at or after the video's end cannot be a real
	// section of it.
	if durationSeconds > 0 && sec >= durationSeconds {
		return out
	}
	if len(out) > 0 && sec <= out[len(out)-1].StartSeconds {
		return out
	}
	return append(out, Chapter{
		StartSeconds: sec,
		Timestamp:    transcript.Timestamp(float64(sec)),
		Title:        title,
	})
}

// chapterSeconds interprets the groups as either mm:ss (three groups, last
// empty) or h:mm:ss, rejecting impossible clock values (seconds or minutes
// above 59) instead of silently normalizing them into the wrong instant.
func chapterSeconds(a, b, c string) (int, bool) {
	first, _ := strconv.Atoi(a)
	second, _ := strconv.Atoi(b)
	if c == "" {
		if second > 59 {
			return 0, false
		}
		return first*60 + second, true
	}
	third, _ := strconv.Atoi(c)
	if second > 59 || third > 59 {
		return 0, false
	}
	return first*3600 + second*60 + third, true
}
