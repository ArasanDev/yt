// yt — YouTube transcripts for agents and humans.
//
//	yt transcript <url> [--lang en,es] [--timestamps] [--format text|json|srt]
//	yt tracks <url>      list available caption tracks
//	yt info <url>        video metadata
//	yt mcp               serve MCP tools over stdio
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	flagLangs      string
	flagTimestamps bool
	flagFormat     string
	flagOutput     string
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
	t, err := FetchTranscript(args[0], parseLangs(flagLangs))
	if err != nil {
		return err
	}
	var out string
	switch flagFormat {
	case "text":
		out = formatText(t, flagTimestamps)
	case "json":
		out, err = formatJSON(t)
		if err != nil {
			return err
		}
	case "srt":
		out = formatSRT(t)
	default:
		return fmt.Errorf("unknown format %q (text|json|srt)", flagFormat)
	}
	fmt.Fprintf(os.Stderr, "via %s | lang=%s generated=%v translated=%v | %d segments\n",
		t.Source, t.Language, t.Generated, t.Translated, len(t.Segments))
	if flagOutput != "" {
		return os.WriteFile(flagOutput, []byte(out+"\n"), 0o644)
	}
	fmt.Println(out)
	return nil
}

func runTracks(cmd *cobra.Command, args []string) error {
	id, err := extractVideoID(args[0])
	if err != nil {
		return err
	}
	pr, via, err := getPlayerResponse(id)
	if err != nil {
		return err
	}
	tracks := pr.tracks()
	if len(tracks) == 0 {
		fmt.Fprintln(os.Stderr, "via "+via)
		return fmt.Errorf("no caption tracks for %s", id)
	}
	fmt.Fprintf(os.Stderr, "via %s\n", via)
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
	pr, via, err := getPlayerResponse(id)
	if err != nil {
		return err
	}
	dur, _ := strconv.Atoi(pr.VideoDetails.LengthSeconds)
	views, _ := strconv.ParseInt(pr.VideoDetails.ViewCount, 10, 64)
	desc := pr.VideoDetails.ShortDescription
	if len(desc) > 500 {
		desc = desc[:500] + "…"
	}
	out := map[string]any{
		"id": pr.VideoDetails.VideoID, "title": pr.VideoDetails.Title,
		"channel": pr.VideoDetails.Author, "channel_id": pr.VideoDetails.ChannelID,
		"duration_seconds": dur, "view_count": views,
		"description": desc, "via": via,
		"url": "https://www.youtube.com/watch?v=" + id,
	}
	s, err := formatJSON(out)
	if err != nil {
		return err
	}
	fmt.Println(s)
	return nil
}

func main() {
	root := &cobra.Command{
		Use:   "yt",
		Short: "YouTube transcripts for agents and humans",
		Long: "Fetch YouTube captions via the Innertube API (ANDROID/IOS clients) — " +
			"no API key, no AI, single static binary.\nRun `yt mcp` to expose the tools over MCP stdio.",
		SilenceUsage: true,
	}

	transcript := &cobra.Command{
		Use:   "transcript <url-or-id>",
		Short: "Fetch a video transcript",
		Args:  cobra.ExactArgs(1),
		RunE:  runTranscript,
	}
	transcript.Flags().StringVarP(&flagLangs, "lang", "l", "en",
		"comma-separated language fallbacks, e.g. en,es")
	transcript.Flags().BoolVarP(&flagTimestamps, "timestamps", "t", false,
		"prefix each segment with [MM:SS]")
	transcript.Flags().StringVarP(&flagFormat, "format", "f", "text",
		"output format: text|json|srt")
	transcript.Flags().StringVarP(&flagOutput, "output", "o", "",
		"write to file instead of stdout")

	tracks := &cobra.Command{
		Use:   "tracks <url-or-id>",
		Short: "List available caption tracks/languages",
		Args:  cobra.ExactArgs(1),
		RunE:  runTracks,
	}

	info := &cobra.Command{
		Use:   "info <url-or-id>",
		Short: "Video metadata (title, channel, duration, views)",
		Args:  cobra.ExactArgs(1),
		RunE:  runInfo,
	}

	mcp := &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP tools over stdio (get_transcript, get_video_info, list_tracks)",
		Args:  cobra.NoArgs,
		RunE:  runMCP,
	}

	root.AddCommand(transcript, tracks, info, mcp)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
