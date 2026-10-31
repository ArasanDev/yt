// Package cli implements yt's cobra commands.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ArasanDev/yt/internal/apperr"
	"github.com/ArasanDev/yt/internal/mcp"
	"github.com/ArasanDev/yt/internal/render"
	"github.com/ArasanDev/yt/internal/search"
	"github.com/ArasanDev/yt/internal/transcript"
	"github.com/ArasanDev/yt/internal/version"
	"github.com/ArasanDev/yt/internal/youtube"
)

// stdinIsTerminal reports whether stdin is an interactive terminal. It is a
// package-level var so tests can inject a value without a real TTY.
var stdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

var (
	flagLangs            string
	flagTimestamps       bool
	flagTimestampMode    string
	flagGranularity      string
	flagFormat           string
	flagOutput           string
	flagStart            float64
	flagEnd              float64
	flagMaxChars         int
	flagStartCue         int
	flagMaxCues          int
	flagBare             bool
	flagDescriptionChars int

	flagChannelLimit   int
	flagChannelSince   string
	flagChannelSummary int
	flagChannelJSON    bool

	flagSearchLangs    string
	flagSearchContext  int
	flagSearchMax      int
	flagSearchStartCue int
	flagSearchMode     string
	flagSearchJSON     bool
)

// validateRange enforces the CLI time-range contract: non-negative bounds and
// a start strictly before end when both are set.
func validateRange(start, end float64) error {
	if start < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --start %v: must be >= 0", start)
	}
	if end < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --end %v: must be >= 0", end)
	}
	if start > 0 && end > 0 && start >= end {
		return apperr.New(apperr.CodeBadRequest, "inverted range: --start %v must be less than --end %v", start, end)
	}
	return nil
}

// validateTranscriptFlags must run before any network fetch so a typo'd flag
// fails instantly instead of after the caption download.
func validateTranscriptFlags(cmd *cobra.Command) error {
	switch flagFormat {
	case "text", "json", "srt":
	default:
		return apperr.New(apperr.CodeBadRequest, "unknown --format %q (text|json|srt)", flagFormat)
	}
	if err := validateRange(flagStart, flagEnd); err != nil {
		return err
	}
	if flagMaxChars < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --max-chars %d: must be >= 0 (0 = unlimited)", flagMaxChars)
	}
	if flagStartCue < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --start-cue %d: must be >= 0", flagStartCue)
	}
	if flagMaxCues < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --max-cues %d: must be >= 0 (0 = unlimited)", flagMaxCues)
	}
	if flagBare && flagFormat != "text" {
		return apperr.New(apperr.CodeBadRequest, "--bare applies only to --format text (got %q)", flagFormat)
	}
	// SRT keeps results raw: the budget, cue and timestamp flags would change
	// nothing, so passing any of them is rejected instead of ignored.
	if flagFormat == "srt" {
		for _, f := range []string{"max-chars", "max-cues", "start-cue", "granularity", "timestamps", "timestamp-mode"} {
			if cmd.Flags().Changed(f) {
				return apperr.New(apperr.CodeBadRequest,
					"--%s does not apply to --format srt; use it with --format text or json", f)
			}
		}
	}
	return nil
}

func runTranscript(client *youtube.Client, args []string) error {
	id, err := youtube.ExtractVideoID(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*youtube.TotalDeadline)
	defer cancel()
	t, err := client.Transcript(ctx, id, youtube.TranscriptOptions{
		Languages:        youtube.ParseLanguages(flagLangs),
		AllowTranslation: true,
	})
	if err != nil {
		return err
	}

	var out string
	switch flagFormat {
	case "text":
		timestampMode := flagTimestampMode
		if timestampMode == "" && flagTimestamps {
			timestampMode = "cue"
		}
		r := render.Render(t, t.DurationSeconds, render.Options{
			Granularity:   flagGranularity,
			TimestampMode: timestampMode,
			StartSeconds:  flagStart,
			EndSeconds:    flagEnd,
			StartCue:      flagStartCue,
			MaxCues:       flagMaxCues,
			MaxChars:      flagMaxChars,
			Bare:          flagBare,
		})
		for _, n := range r.Notes {
			fmt.Fprintln(os.Stderr, "note:", n)
		}
		// Bare output keeps stdout clean; truncation must still be visible,
		// so for --bare it moves to stderr instead of disappearing.
		if flagBare && r.Truncated && r.NextStartSeconds != nil && r.NextCueIndex != nil {
			fmt.Fprintf(os.Stderr, "truncated: cues=%d/%d; resume with --start-cue %d or --start %s\n",
				r.ReturnedCues, r.TotalCues, *r.NextCueIndex,
				strconv.FormatFloat(*r.NextStartSeconds, 'f', -1, 64))
		}
		out = r.Text
	case "json":
		js := render.SelectForJSON(t.Segments, t.DurationSeconds, render.Options{
			StartSeconds: flagStart,
			EndSeconds:   flagEnd,
			StartCue:     flagStartCue,
			MaxCues:      flagMaxCues,
			MaxChars:     flagMaxChars,
		})
		subset := *t
		subset.Segments = js.Segments
		doc := struct {
			*transcript.Transcript
			Truncated        bool     `json:"truncated"`
			NextStartSeconds *float64 `json:"next_start_seconds,omitempty"`
			NextCueIndex     *int     `json:"next_cue_index,omitempty"`
		}{&subset, js.Truncated, js.NextStartSeconds, js.NextCueIndex}
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		out = string(b)
	case "srt":
		segs := render.Select(t.Segments, render.Options{StartSeconds: flagStart, EndSeconds: flagEnd})
		out = render.FormatSRT(&transcript.Transcript{Segments: segs})
	default:
		return apperr.New(apperr.CodeBadRequest, "unknown --format %q (text|json|srt)", flagFormat)
	}

	fmt.Fprintf(os.Stderr, "via %s | lang=%s original=%s generated=%v translated=%v | %d segments\n",
		t.Source, t.Language, t.OriginalLanguage, t.Generated, t.Translated, len(t.Segments))
	if flagOutput != "" {
		return os.WriteFile(flagOutput, []byte(out+"\n"), 0o644)
	}
	fmt.Println(out)
	return nil
}

// runSearch answers "where does this video talk about X" without pulling the
// whole transcript into the caller's context.
func runSearch(client *youtube.Client, args []string) error {
	id, err := youtube.ExtractVideoID(args[0])
	if err != nil {
		return err
	}
	if flagSearchStartCue < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --start-cue %d: must be >= 0", flagSearchStartCue)
	}
	if flagSearchContext < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --context-cues %d: must be >= 0", flagSearchContext)
	}
	if flagSearchMax < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --max-matches %d: must be >= 0", flagSearchMax)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*youtube.TotalDeadline)
	defer cancel()
	mode, ok := search.ParseMode(flagSearchMode)
	if !ok {
		return apperr.New(apperr.CodeBadRequest, "unknown match mode %q (all|any|phrase)", flagSearchMode)
	}
	t, err := client.Transcript(ctx, id, youtube.TranscriptOptions{
		Languages:        youtube.ParseLanguages(flagSearchLangs),
		AllowTranslation: true,
	})
	if err != nil {
		return err
	}
	res := search.Segments(t.Segments, search.Options{
		Query:       args[1],
		ContextCues: flagSearchContext,
		MaxMatches:  flagSearchMax,
		StartCue:    flagSearchStartCue,
		MatchMode:   mode,
	})

	if flagSearchJSON {
		out := struct {
			VideoID   string `json:"video_id"`
			Language  string `json:"language"`
			Generated bool   `json:"generated"`
			*search.Result
		}{t.VideoID, t.Language, t.Generated, &res}
		s, err := formatJSON(out)
		if err != nil {
			return err
		}
		fmt.Println(s)
		return nil
	}

	fmt.Fprintf(os.Stderr, "%s | lang=%s generated=%v | %d matches (%s)\n",
		t.VideoID, t.Language, t.Generated, res.TotalMatches, res.MatchMode)
	for _, m := range res.Matches {
		// ContextCues is computed search-side; surface it in human output as
		// indented lines around the match.
		if m.Before != "" {
			fmt.Printf("    … %s\n", m.Before)
		}
		fmt.Printf("[%s] %s\n", m.Timestamp, m.Text)
		if m.After != "" {
			fmt.Printf("    … %s\n", m.After)
		}
	}
	for _, n := range res.Notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	return nil
}

func runTracks(client *youtube.Client, args []string) error {
	id, err := youtube.ExtractVideoID(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), youtube.TotalDeadline)
	defer cancel()
	via, tracks, err := client.Tracks(ctx, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "via %s\n", via)
	if len(tracks) == 0 {
		fmt.Println("no caption tracks")
		return nil
	}
	for _, t := range tracks {
		kind := "manual"
		if t.Kind == "asr" {
			kind = "asr"
		}
		fmt.Printf("%-8s %-6s translatable=%-5v %s\n",
			t.Language, kind, t.Translatable, t.Name)
	}
	return nil
}

func runInfo(client *youtube.Client, args []string) error {
	id, err := youtube.ExtractVideoID(args[0])
	if err != nil {
		return err
	}
	if flagDescriptionChars < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --description-chars %d: must be >= 0 (0 = full)", flagDescriptionChars)
	}
	ctx, cancel := context.WithTimeout(context.Background(), youtube.TotalDeadline)
	defer cancel()
	info, err := client.Info(ctx, id, flagDescriptionChars)
	if err != nil {
		return err
	}
	s, err := formatJSON(info)
	if err != nil {
		return err
	}
	fmt.Println(s)
	return nil
}

func runChannel(client *youtube.Client, args []string) error {
	if flagChannelLimit < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --limit %d: must be >= 0 (0 = all; the feed exposes at most 15)", flagChannelLimit)
	}
	if flagChannelSummary < 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid --description-chars %d: must be >= 0 (0 = full)", flagChannelSummary)
	}
	var since time.Time
	if strings.TrimSpace(flagChannelSince) != "" {
		t, err := youtube.ParseSince(flagChannelSince)
		if err != nil {
			return err
		}
		since = t
	}
	ctx, cancel := context.WithTimeout(context.Background(), youtube.TotalDeadline)
	defer cancel()
	listing, err := client.Channel(ctx, args[0], youtube.ChannelOptions{
		Limit:        flagChannelLimit,
		Since:        since,
		SummaryChars: flagChannelSummary,
	})
	if err != nil {
		return err
	}

	if flagChannelJSON {
		s, err := formatJSON(listing)
		if err != nil {
			return err
		}
		fmt.Println(s)
		return nil
	}

	fmt.Fprintf(os.Stderr, "%s (%s) — %d of %d videos", listing.Channel.Title, listing.Channel.ID,
		listing.Count, listing.Available)
	if listing.NewestPublished != "" {
		fmt.Fprintf(os.Stderr, ", newest %s", listing.NewestPublished)
	}
	fmt.Fprintln(os.Stderr)
	for _, v := range listing.Videos {
		fmt.Printf("%s  %s  %s\n", publishedShort(v.Published), v.URL, v.Title)
		if v.Summary != "" {
			fmt.Printf("    %s\n", v.Summary)
		}
	}
	if listing.Count == 0 {
		fmt.Println("no videos")
	}
	for _, n := range listing.Notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	return nil
}

func publishedShort(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

func runDoctor(client *youtube.Client, args []string) error {
	const probe = "dQw4w9WgXcQ"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	report := client.Doctor(ctx, probe)
	s, err := formatJSON(report)
	if err != nil {
		return err
	}
	fmt.Println(s)
	return nil
}

func formatJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// NewRootCmd builds the yt command tree. The client is injected so tests and
// embedders can use another acquisition origin.
func NewRootCmd(client *youtube.Client) *cobra.Command {
	root := &cobra.Command{
		Use:     "yt",
		Version: version.String(),
		Short:   "YouTube transcripts for agents and humans",
		Long: "Fetch YouTube captions via the Innertube API (ANDROID/IOS clients) — " +
			"no API key, no AI, single static binary.\n" +
			"Run `yt mcp` (or `yt` with piped stdin) to expose the tools over MCP stdio.",
		SilenceUsage: true,
		// No subcommand: serve MCP when stdin is a pipe (an MCP client), help at a TTY.
		RunE: func(cmd *cobra.Command, args []string) error {
			if stdinIsTerminal() {
				return cmd.Help()
			}
			return mcp.Run(client)
		},
	}

	transcriptCmd := &cobra.Command{
		Use:   "transcript <url-or-id>",
		Short: "Fetch a video transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTranscriptFlags(cmd); err != nil {
				return err
			}
			return runTranscript(client, args)
		},
	}
	transcriptCmd.Flags().StringVarP(&flagLangs, "lang", "l", "en",
		"comma-separated language fallbacks, e.g. en,es, or 'original'")
	transcriptCmd.Flags().BoolVarP(&flagTimestamps, "timestamps", "t", false,
		"legacy alias: prefix each line with [MM:SS]")
	transcriptCmd.Flags().StringVar(&flagTimestampMode, "timestamp-mode", "",
		"none|block (default)|cue")
	transcriptCmd.Flags().StringVar(&flagGranularity, "granularity", "block",
		"cue|block (default)")
	transcriptCmd.Flags().StringVarP(&flagFormat, "format", "f", "text",
		"output format: text|json|srt (text: metadata header + plain body; "+
			"srt rejects --max-chars/--max-cues/--start-cue/--granularity and the timestamp flags)")
	transcriptCmd.Flags().StringVarP(&flagOutput, "output", "o", "",
		"write to file instead of stdout")
	transcriptCmd.Flags().Float64Var(&flagStart, "start", 0,
		"start time in seconds; resume from next_start_seconds")
	transcriptCmd.Flags().Float64Var(&flagEnd, "end", 0,
		"end time in seconds (cues starting at or after it are excluded)")
	transcriptCmd.Flags().IntVar(&flagMaxChars, "max-chars", 0,
		"limit transcript body characters (0 = unlimited; this CLI has no default cap, "+
			"while the MCP get_transcript tool defaults to 50000)")
	transcriptCmd.Flags().IntVar(&flagStartCue, "start-cue", 0,
		"skip cues before this index (0 = from the first cue)")
	transcriptCmd.Flags().IntVar(&flagMaxCues, "max-cues", 0,
		"return at most N cues (0 = unlimited); truncation is reported with resume pointers")
	transcriptCmd.Flags().BoolVar(&flagBare, "bare", false,
		"text format only: omit the metadata header and truncation trailer so stdout pipes cleanly; "+
			"truncation is reported on stderr")

	searchCmd := &cobra.Command{
		Use:   "search <url-or-id> <query>",
		Short: "Find a topic in a transcript (timestamps, no full read)",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSearch(client, args)
		},
	}
	searchCmd.Flags().StringVarP(&flagSearchLangs, "lang", "l", "en",
		"comma-separated language fallbacks, e.g. en,es, or 'original'")
	searchCmd.Flags().IntVar(&flagSearchContext, "context-cues", search.DefaultContextCues,
		"cues of context before and after each match")
	searchCmd.Flags().IntVar(&flagSearchMax, "max-matches", search.DefaultMaxMatches,
		"maximum matches to return (max 50)")
	searchCmd.Flags().IntVar(&flagSearchStartCue, "start-cue", 0,
		"skip matches before this cue index")
	searchCmd.Flags().StringVar(&flagSearchMode, "match-mode", string(search.All),
		"how words match: all|any|phrase")
	searchCmd.Flags().BoolVar(&flagSearchJSON, "json", false,
		"print structured JSON")

	tracksCmd := &cobra.Command{
		Use:   "tracks <url-or-id>",
		Short: "List available caption tracks/languages",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runTracks(client, args)
		},
	}
	infoCmd := &cobra.Command{
		Use:   "info <url-or-id>",
		Short: "Video metadata (title, channel, duration, views, captions)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runInfo(client, args)
		},
	}
	infoCmd.Flags().IntVar(&flagDescriptionChars, "description-chars", youtube.DefaultDescriptionChars,
		"max characters of the description (0 = full)")

	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check which YouTube acquisition clients still work",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDoctor(client, nil)
		},
	}

	channelCmd := &cobra.Command{
		Use:   "channel <handle|url>",
		Short: "List a channel's latest uploads (exact timestamps)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runChannel(client, args)
		},
	}
	channelCmd.Flags().IntVar(&flagChannelLimit, "limit", youtube.DefaultChannelLimit,
		"max videos to return (0 = all; the feed exposes at most 15)")
	channelCmd.Flags().StringVar(&flagChannelSince, "since", "",
		"only videos published after this UTC time (RFC3339 or YYYY-MM-DD)")
	channelCmd.Flags().IntVar(&flagChannelSummary, "description-chars", youtube.DefaultSummaryChars,
		"characters of the description excerpt (0 = omit)")
	channelCmd.Flags().BoolVar(&flagChannelJSON, "json", false,
		"print structured JSON")

	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP tools over stdio (get_video_info, get_transcript, search_transcript, list_tracks, list_channel_videos)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return mcp.Run(client)
		},
	}

	root.AddCommand(transcriptCmd, searchCmd, tracksCmd, infoCmd, doctorCmd, channelCmd, mcpCmd)
	return root
}
