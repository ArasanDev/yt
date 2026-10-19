// MCP server: exposes yt tools over stdio via the official Go SDK.
// Wire into agents with: {"command": "yt", "args": ["mcp"]}
package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

type transcriptArgs struct {
	URLOrID    string `json:"url_or_id"           jsonschema:"YouTube URL (watch, youtu.be, shorts, live, embed) or bare 11-char video ID"`
	Languages  string `json:"languages,omitempty"  jsonschema:"comma-separated ISO codes tried in order, e.g. 'en' or 'en,es' (default en)"`
	Timestamps bool   `json:"timestamps,omitempty" jsonschema:"prefix each line with [MM:SS] to cite positions"`
}

type urlArg struct {
	URLOrID string `json:"url_or_id" jsonschema:"YouTube URL (watch, youtu.be, shorts, live, embed) or bare 11-char video ID"`
}

func transcriptTool(_ context.Context, _ *mcp.CallToolRequest, a transcriptArgs) (*mcp.CallToolResult, any, error) {
	t, err := FetchTranscript(a.URLOrID, parseLangs(a.Languages))
	if err != nil {
		return nil, nil, err
	}
	header := fmt.Sprintf("# transcript | video=%s source=%s language=%s auto_generated=%v translated=%v",
		t.VideoID, t.Source, t.Language, t.Generated, t.Translated)
	out := header + "\n\n" + formatText(t, a.Timestamps)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: out}},
	}, nil, nil
}

func videoInfoTool(_ context.Context, _ *mcp.CallToolRequest, a urlArg) (*mcp.CallToolResult, any, error) {
	id, err := extractVideoID(a.URLOrID)
	if err != nil {
		return nil, nil, err
	}
	pr, via, err := getPlayerResponse(id)
	if err != nil {
		return nil, nil, err
	}
	dur, _ := strconv.Atoi(pr.VideoDetails.LengthSeconds)
	views, _ := strconv.ParseInt(pr.VideoDetails.ViewCount, 10, 64)
	desc := pr.VideoDetails.ShortDescription
	if len(desc) > 500 {
		desc = desc[:500] + "…"
	}
	s, err := formatJSON(map[string]any{
		"id": pr.VideoDetails.VideoID, "title": pr.VideoDetails.Title,
		"channel": pr.VideoDetails.Author, "channel_id": pr.VideoDetails.ChannelID,
		"duration_seconds": dur, "view_count": views,
		"description": desc, "via": via,
		"url": "https://www.youtube.com/watch?v=" + id,
	})
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: s}},
	}, nil, nil
}

func tracksTool(_ context.Context, _ *mcp.CallToolRequest, a urlArg) (*mcp.CallToolResult, any, error) {
	id, err := extractVideoID(a.URLOrID)
	if err != nil {
		return nil, nil, err
	}
	pr, via, err := getPlayerResponse(id)
	if err != nil {
		return nil, nil, err
	}
	tracks := pr.tracks()
	if len(tracks) == 0 {
		return nil, nil, fmt.Errorf("no caption tracks for %s", id)
	}
	type trackInfo struct {
		Language     string `json:"language"`
		Kind         string `json:"kind"` // manual | asr (auto-generated)
		Translatable bool   `json:"translatable"`
		Name         string `json:"name"`
	}
	list := make([]trackInfo, 0, len(tracks))
	for _, t := range tracks {
		kind := "manual"
		if t.Kind == "asr" {
			kind = "asr"
		}
		list = append(list, trackInfo{t.LanguageCode, kind, t.IsTranslatable, t.trackName()})
	}
	s, err := formatJSON(map[string]any{"video_id": id, "via": via, "tracks": list})
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: s}},
	}, nil, nil
}

func runMCP(_ *cobra.Command, _ []string) error {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "yt", Version: "v0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_transcript",
		Description: "Fetch the spoken transcript of a YouTube video from its caption " +
			"tracks (manual or auto-generated). Pass languages to prefer a language; " +
			"auto-translates into the first language if needed.",
	}, transcriptTool)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_video_info",
		Description: "Get metadata for a YouTube video without downloading it: title, " +
			"channel, duration, view count, description excerpt. Good for triaging " +
			"before fetching a transcript.",
	}, videoInfoTool)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_tracks",
		Description: "List the caption tracks a YouTube video has: language codes, " +
			"manual vs auto-generated, and whether auto-translation is available.",
	}, tracksTool)

	return server.Run(context.Background(), &mcp.StdioTransport{})
}
