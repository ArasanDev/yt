// Chapters are authored structure that already travels inside the description.
// Parsing them costs one regex pass and turns a 50-minute talk into addressable
// sections, which is what reading and citation plans actually need.
package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Chapter is an author-supplied section marker.
type Chapter struct {
	StartSeconds int    `json:"start_seconds"`
	Timestamp    string `json:"timestamp"`
	Title        string `json:"title"`
}

var (
	chapterHeadingPattern = regexp.MustCompile(`(?i)^\s*(chapters?|timestamps?)\s*:?\s*$`)
	// Accepts "00:00 Title", "00:00 - Title", "0:00:00 Title", "* 00:00 Title".
	chapterLinePattern = regexp.MustCompile(`^\s*[-*•]?\s*(\d{1,2}):(\d{2})(?::(\d{2}))?\s*[-–—:)]?\s*(.+?)\s*$`)
)

// parseChapters extracts a chapter list from a video description. It returns
// nil unless the list looks trustworthy: a headings-anchored block of at least
// two strictly increasing timestamps that fit inside the video.
func parseChapters(desc string, durationSeconds int) []Chapter {
	lines := strings.Split(desc, "\n")
	start := -1
	for i, line := range lines {
		if chapterHeadingPattern.MatchString(line) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}

	var out []Chapter
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
		title := strings.TrimSpace(m[4])
		if title == "" {
			continue
		}
		sec := chapterSeconds(m[1], m[2], m[3])
		out = append(out, Chapter{
			StartSeconds: sec,
			Timestamp:    tsShort(float64(sec)),
			Title:        title,
		})
		if len(out) >= 200 {
			break
		}
	}

	if len(out) < 2 {
		return nil
	}
	for i := 1; i < len(out); i++ {
		if out[i].StartSeconds <= out[i-1].StartSeconds {
			return nil
		}
	}
	// A stale description can outlive an edit; reject lists that overshoot the
	// current duration by more than two minutes.
	if durationSeconds > 0 && out[len(out)-1].StartSeconds > durationSeconds+120 {
		return nil
	}
	return out
}

// chapterSeconds interprets the groups as either mm:ss (three groups, last
// empty) or h:mm:ss.
func chapterSeconds(a, b, c string) int {
	first, _ := strconv.Atoi(a)
	second, _ := strconv.Atoi(b)
	if c == "" {
		return first*60 + second
	}
	third, _ := strconv.Atoi(c)
	return first*3600 + second*60 + third
}
