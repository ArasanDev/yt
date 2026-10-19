# yt

YouTube transcripts for agents and humans. One static Go binary, no API key,
no AI dependencies, no runtime requirements.

- Fetches a video's own caption tracks (manual or auto-generated) via YouTube's
  internal **Innertube API**, using `ANDROID`/`IOS` client contexts — the
  WEB-client caption URLs have been pot-token gated since mid-2025 and return
  empty bodies.
- Cascades `innertube-android → innertube-ios → watch-page` so a single
  throttled endpoint doesn't take the fetch down; reports which method won.
- Same binary is the MCP server (`yt mcp`).

Dependencies: `cobra` (CLI) + `modelcontextprotocol/go-sdk` (official MCP SDK).
The YouTube fetch layer is stdlib only — `net/http`, `encoding/json`,
`encoding/xml`.

## Install

```sh
go build -o yt . && cp yt /opt/homebrew/bin/yt   # or any dir on PATH
```

## CLI

```sh
yt transcript <url-or-id>                    # transcript to stdout
yt transcript <url> -l en,es -t              # language fallback + [MM:SS] stamps
yt transcript <url> -f srt -o subs.srt       # SRT file
yt tracks <url>                              # list caption languages
yt info <url>                                # title, channel, duration, views
```

Accepts `watch?v=`, `youtu.be/`, `/shorts/`, `/live/`, `/embed/` URLs or a bare
11-char video ID. Fetch details (method, language, generated/translated) go to
stderr so stdout stays clean for piping.

## MCP

`yt mcp` serves stdio JSON-RPC with three tools:

| Tool | Returns |
|---|---|
| `get_transcript(url_or_id, languages?, timestamps?)` | Transcript text + provenance header |
| `get_video_info(url_or_id)` | JSON: title, channel, duration, views, description |
| `list_tracks(url_or_id)` | JSON: caption languages, manual vs asr, translatable |

Wire into any agent:

```sh
devin mcp add yt -- yt mcp          # Devin CLI
claude mcp add yt -- yt mcp         # Claude Code
```

or raw `mcpServers` JSON (Devin, Claude Desktop, Cursor, Windsurf, …):

```json
{"mcpServers": {"yt": {"command": "yt", "args": ["mcp"]}}}
```

## Caveats

- **IP blocking**: datacenter IPs (AWS/GCP/VPS) get blocked or rate-limited by
  YouTube. From a residential IP — your Mac — it just works. For a server,
  cross-compile (`GOOS=linux GOARCH=arm64 go build`) and expect to need a
  residential proxy eventually.
- Auto-generated (`asr`) captions lack punctuation — fine for agents. Manual
  tracks are preferred automatically; YouTube auto-translation is used when the
  requested language has no track.
- Age-restricted / private videos need auth — not supported.

## python/

Earlier Python prototype (`youtube-transcript-api` + optional faster-whisper
fallback for uncaptioned videos). Kept for the whisper path; the Go binary is
the primary tool.
