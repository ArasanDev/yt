// Package transcript holds the caption model shared by acquisition, search,
// rendering, and the adapters.
package transcript

// Segment is one caption cue.
type Segment struct {
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
	Text     string  `json:"text"`
}

// Transcript is a downloaded caption track plus its provenance.
type Transcript struct {
	VideoID            string    `json:"video_id"`
	Source             string    `json:"source"` // innertube-android | innertube-ios | watch-page
	Language           string    `json:"language"`
	OriginalLanguage   string    `json:"original_language"`
	TranslationTarget  string    `json:"translation_target,omitempty"`
	RequestedLanguages []string  `json:"requested_languages,omitempty"`
	Generated          bool      `json:"generated"`
	Translated         bool      `json:"translated"`
	DurationSeconds    int       `json:"duration_seconds"`
	Segments           []Segment `json:"segments"`
}
