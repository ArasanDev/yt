// Package cli implements yt's cobra commands.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"yt/internal/mcp"
	"yt/internal/render"
	"yt/internal/search"
	"yt/internal/transcript"
	"yt/internal/youtube"
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
			MaxChars:      flagMaxChars,
		})
		for _, n := range r.Notes {
			fmt.Fprintln(os.Stderr, "note:", n)
		}
		out = r.Text
	case "json":
		js := render.SelectForJSON(t.Segments, t.DurationSeconds, render.Options{
			StartSeconds: flagStart,
			EndSeconds:   flagEnd,
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
		return fmt.Errorf("unknown format %q (text|json|srt)", flagFormat)
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*youtube.TotalDeadline)
	defer cancel()
	mode, ok := search.ParseMode(flagSearchMode)
	if !ok {
		return fmt.Errorf("unknown match mode %q (all|any|phrase)", flagSearchMode)
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
		fmt.Printf("[%s] %s\n", m.Timestamp, m.Text)
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
		Use:   "yt",
		Short: "YouTube transcripts for agents and humans",
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
		RunE: func(_ *cobra.Command, args []string) error {
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
		"output format: text|json|srt")
	transcriptCmd.Flags().StringVarP(&flagOutput, "output", "o", "",
		"write to file instead of stdout")
	transcriptCmd.Flags().Float64Var(&flagStart, "start", 0, "start time in seconds")
	transcriptCmd.Flags().Float64Var(&flagEnd, "end", 0, "end time in seconds")
	transcriptCmd.Flags().IntVar(&flagMaxChars, "max-chars", 0,
		"limit transcript characters (0 = unlimited)")

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
		Short: "Serve MCP tools over stdio (get_video_info, get_transcript, search_transcript, list_tracks)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return mcp.Run(client)
		},
	}

	root.AddCommand(transcriptCmd, searchCmd, tracksCmd, infoCmd, doctorCmd, channelCmd, mcpCmd)
	return root
}
