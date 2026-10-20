// Output formatters for transcript text.
package render

import (
	"fmt"
	"strings"

	"yt/internal/transcript"
)

// FormatSRT renders a transcript as SubRip subtitles.
func FormatSRT(t *transcript.Transcript) string {
	var b strings.Builder
	for i, s := range t.Segments {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, transcript.TimestampSRT(s.Start), transcript.TimestampSRT(s.Start+s.Duration), s.Text)
	}
	return b.String()
}
