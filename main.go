// yt — YouTube transcripts for agents and humans.
//
//	yt transcript <url> [--lang en,es] [--timestamps] [--format text|json|srt]
//	yt search <url> <query> [--match-mode all|any|phrase] find a topic with timestamps
//	yt tracks <url>      list available caption tracks
//	yt info <url>        video metadata
//	yt channel <handle>  list a channel's latest uploads (RSS, exact timestamps)
//	yt doctor            check which YouTube clients still work
//	yt mcp               serve MCP tools over stdio (default when stdin is piped)
//
// With no subcommand, yt serves MCP when stdin is not a terminal (the shape an
// MCP client spawns) and prints help when it is. `yt mcp` is always explicit.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
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

func parseLangs(csv string) []string {
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

func runTranscript(cmd *cobra.Command, args []string) error {
	id, err := extractVideoID(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*totalDeadline)
	defer cancel()
	t, err := defaultClient.Transcript(ctx, id, TranscriptOptions{
		Languages:        parseLangs(flagLangs),
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
		r := Render(t, t.DurationSeconds, RenderOptions{
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
		js := selectForJSON(t.Segments, t.DurationSeconds, RenderOptions{
			StartSeconds: flagStart,
			EndSeconds:   flagEnd,
			MaxChars:     flagMaxChars,
		})
		subset := *t
		subset.Segments = js.Segments
		doc := struct {
			*Transcript
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
		segs := selectSegments(t.Segments, RenderOptions{StartSeconds: flagStart, EndSeconds: flagEnd})
		filtered := &Transcript{Segments: make([]Segment, 0, len(segs))}
		for _, s := range segs {
			filtered.Segments = append(filtered.Segments, s.seg)
		}
		out = formatSRT(filtered)
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
func runSearch(cmd *cobra.Command, args []string) error {
	id, err := extractVideoID(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*totalDeadline)
	defer cancel()
	mode, ok := parseMatchMode(flagSearchMode)
	if !ok {
		return fmt.Errorf("unknown match mode %q (all|any|phrase)", flagSearchMode)
	}
	t, res, err := defaultClient.SearchTranscript(ctx, id,
		TranscriptOptions{Languages: parseLangs(flagSearchLangs), AllowTranslation: true},
		SearchOptions{
			Query:       args[1],
			ContextCues: flagSearchContext,
			MaxMatches:  flagSearchMax,
			StartCue:    flagSearchStartCue,
			MatchMode:   mode,
		})
	if err != nil {
		return err
	}

	if flagSearchJSON {
		out := struct {
			VideoID   string `json:"video_id"`
			Language  string `json:"language"`
			Generated bool   `json:"generated"`
			*SearchResult
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

func runTracks(cmd *cobra.Command, args []string) error {
	id, err := extractVideoID(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), totalDeadline)
	defer cancel()
	_, via, tracks, err := defaultClient.Tracks(ctx, id)
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
			t.LanguageCode, kind, t.IsTranslatable, t.trackName())
	}
	return nil
}

func runInfo(cmd *cobra.Command, args []string) error {
	id, err := extractVideoID(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), totalDeadline)
	defer cancel()
	info, err := defaultClient.Info(ctx, id, flagDescriptionChars)
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

func runChannel(cmd *cobra.Command, args []string) error {
	var since time.Time
	if strings.TrimSpace(flagChannelSince) != "" {
		t, err := parseSince(flagChannelSince)
		if err != nil {
			return err
		}
		since = t
	}
	ctx, cancel := context.WithTimeout(context.Background(), totalDeadline)
	defer cancel()
	listing, err := defaultClient.Channel(ctx, args[0], ChannelOptions{
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

func runDoctor(cmd *cobra.Command, args []string) error {
	const probe = "dQw4w9WgXcQ"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var results []map[string]any
	okCount := 0
	start := time.Now()
	for _, c := range clientContexts {
		entry := map[string]any{"client": c.Name, "version": c.Version}
		t0 := time.Now()
		pr, err := innertubePlayer(ctx, probe, c)
		if err != nil {
			entry["ok"] = false
			if te, ok := toolErrorOf(err); ok {
				entry["code"] = te.Code
				entry["error"] = te.Message
			} else {
				entry["error"] = err.Error()
			}
		} else {
			entry["ok"] = true
			entry["title"] = pr.VideoDetails.Title
			entry["caption_tracks"] = len(pr.tracks())
			entry["ms"] = time.Since(t0).Milliseconds()
			okCount++
		}
		results = append(results, entry)
	}
	{
		entry := map[string]any{"client": "WATCH_PAGE"}
		t0 := time.Now()
		pr, err := watchPagePlayer(ctx, probe)
		if err != nil {
			entry["ok"] = false
			if te, ok := toolErrorOf(err); ok {
				entry["code"] = te.Code
				entry["error"] = te.Message
			} else {
				entry["error"] = err.Error()
			}
		} else {
			entry["ok"] = true
			entry["title"] = pr.VideoDetails.Title
			entry["caption_tracks"] = len(pr.tracks())
			entry["ms"] = time.Since(t0).Milliseconds()
			okCount++
		}
		results = append(results, entry)
	}

	summary := map[string]any{
		"probe_video": probe,
		"ok_clients":  okCount,
		"total_ms":    time.Since(start).Milliseconds(),
		"results":     results,
		"verdict":     "healthy",
	}
	if okCount == 0 {
		summary["verdict"] = "all_clients_failed"
	}
	s, err := formatJSON(summary)
	if err != nil {
		return err
	}
	fmt.Println(s)
	return nil
}

func newRootCmd() *cobra.Command {
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
			return runMCP(cmd, args)
		},
	}

	transcript := &cobra.Command{
		Use:   "transcript <url-or-id>",
		Short: "Fetch a video transcript",
		Args:  cobra.ExactArgs(1),
		RunE:  runTranscript,
	}
	transcript.Flags().StringVarP(&flagLangs, "lang", "l", "en",
		"comma-separated language fallbacks, e.g. en,es, or 'original'")
	transcript.Flags().BoolVarP(&flagTimestamps, "timestamps", "t", false,
		"legacy alias: prefix each line with [MM:SS]")
	transcript.Flags().StringVar(&flagTimestampMode, "timestamp-mode", "",
		"none|block (default)|cue")
	transcript.Flags().StringVar(&flagGranularity, "granularity", "block",
		"cue|block (default)")
	transcript.Flags().StringVarP(&flagFormat, "format", "f", "text",
		"output format: text|json|srt")
	transcript.Flags().StringVarP(&flagOutput, "output", "o", "",
		"write to file instead of stdout")
	transcript.Flags().Float64Var(&flagStart, "start", 0, "start time in seconds")
	transcript.Flags().Float64Var(&flagEnd, "end", 0, "end time in seconds")
	transcript.Flags().IntVar(&flagMaxChars, "max-chars", 0,
		"limit transcript characters (0 = unlimited)")

	search := &cobra.Command{
		Use:   "search <url-or-id> <query>",
		Short: "Find a topic in a transcript (timestamps, no full read)",
		Args:  cobra.ExactArgs(2),
		RunE:  runSearch,
	}
	search.Flags().StringVarP(&flagSearchLangs, "lang", "l", "en",
		"comma-separated language fallbacks, e.g. en,es, or 'original'")
	search.Flags().IntVar(&flagSearchContext, "context-cues", defaultContextCues,
		"cues of context before and after each match")
	search.Flags().IntVar(&flagSearchMax, "max-matches", defaultMaxMatches,
		"maximum matches to return (max 50)")
	search.Flags().IntVar(&flagSearchStartCue, "start-cue", 0,
		"skip matches before this cue index")
	search.Flags().StringVar(&flagSearchMode, "match-mode", string(MatchAll),
		"how words match: all|any|phrase")
	search.Flags().BoolVar(&flagSearchJSON, "json", false,
		"print structured JSON")

	tracks := &cobra.Command{
		Use:   "tracks <url-or-id>",
		Short: "List available caption tracks/languages",
		Args:  cobra.ExactArgs(1),
		RunE:  runTracks,
	}
	info := &cobra.Command{
		Use:   "info <url-or-id>",
		Short: "Video metadata (title, channel, duration, views, captions)",
		Args:  cobra.ExactArgs(1),
		RunE:  runInfo,
	}
	info.Flags().IntVar(&flagDescriptionChars, "description-chars", 500,
		"max characters of the description (0 = full)")

	doctor := &cobra.Command{
		Use:   "doctor",
		Short: "Check which YouTube acquisition clients still work",
		Args:  cobra.NoArgs,
		RunE:  runDoctor,
	}

	channel := &cobra.Command{
		Use:   "channel <handle|url>",
		Short: "List a channel's latest uploads (exact timestamps)",
		Args:  cobra.ExactArgs(1),
		RunE:  runChannel,
	}
	channel.Flags().IntVar(&flagChannelLimit, "limit", defaultChannelLimit,
		"max videos to return (0 = all; the feed exposes at most 15)")
	channel.Flags().StringVar(&flagChannelSince, "since", "",
		"only videos published after this UTC time (RFC3339 or YYYY-MM-DD)")
	channel.Flags().IntVar(&flagChannelSummary, "description-chars", defaultSummaryChars,
		"characters of the description excerpt (0 = omit)")
	channel.Flags().BoolVar(&flagChannelJSON, "json", false,
		"print structured JSON")

	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP tools over stdio (get_video_info, get_transcript, search_transcript, list_tracks)",
		Args:  cobra.NoArgs,
		RunE:  runMCP,
	}

	root.AddCommand(transcript, search, tracks, info, doctor, channel, mcpCmd)
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
