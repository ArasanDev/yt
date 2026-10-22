# yt

YouTube transcripts for agents and humans. One static Go binary, no API key,
no AI dependencies, no runtime requirements.

- Fetches a video's own caption tracks (manual or auto-generated) via YouTube's
  internal **Innertube API**, using `ANDROID`/`IOS` client contexts — the
  WEB-client caption URLs have been pot-token gated since mid-2025 and return
  empty bodies.
- Cascades `innertube-android → innertube-ios → watch-page` so a single
  throttled endpoint doesn't take the fetch down; every response reports which
  method won (`source`).
- Channel monitoring: `yt channel @handle` (or `list_channel_videos`) resolves a
  handle and lists the latest uploads with **exact** publish timestamps from the
  channel RSS feed, so an agent can diff against a watermark and see what is new.
- Same binary is the MCP server: `yt mcp`, or bare `yt` when stdin is piped
  (the shape MCP clients spawn). Bare `yt` at a terminal prints help.
- MCP responses are designed for agent context budgets: bounded payloads,
  structural truncation with **exact** resume pointers, time/cue-addressed
  ranges, server-side search, typed errors, and session-level caching so one
  `get_video_info` + `get_transcript` + `search_transcript` share a single
  player fetch.

Dependencies: `cobra` (CLI), `modelcontextprotocol/go-sdk` (official MCP SDK),
and `golang.org/x/term` (TTY detection). The YouTube fetch layer is stdlib only
— `net/http`, `encoding/json`, `encoding/xml`.

## Layout

The repository root keeps only the product folder and repository metadata
(`.git`, `.devin`). Everything else lives here.

```
yt/                  module and product root
  cmd/yt/            binary entry point
  internal/cli/      cobra commands and flags
  internal/mcp/      MCP tool schemas, handlers, safety warnings
  internal/youtube/  Innertube client, captions, channels, cache, typed failures
  internal/search/   pure transcript search
  internal/render/   block/cue rendering, SRT, truncation metadata
  internal/transcript/ shared Segment/Transcript model and timestamp helpers
  internal/apperr/   typed error contract (code, retryable, suggestion)
```

Dependencies point inward: `cli` and `mcp` depend on `youtube`, `search`,
`render`, and `transcript`; `youtube` depends on `apperr` and `transcript`;
`search` and `render` depend only on `transcript`. The acquisition layer does
not import the adapters.

## Install

From the repository root:

```sh
cd yt
go build -o bin/yt ./cmd/yt
rm -f /opt/homebrew/bin/yt && cp bin/yt /opt/homebrew/bin/yt  # or any dir on PATH
# Replace (rm+cp), don't overwrite a running binary in place:
#   cp over a mapped executable makes macOS SIGKILL the next exec.
```

`go install ./cmd/yt` also works from `yt/`. The module path is the local `yt`;
change it to a hosted path first if you need to import these packages from
another module.

## CLI

```sh
yt transcript <url-or-id>                       # transcript to stdout
yt transcript <url> -l en,es -t                 # language fallback + [MM:SS] stamps
yt transcript <url> --start 1274 --end 1290     # time range
yt transcript <url> -f srt -o subs.srt          # SRT file
yt transcript <url> -f json --max-chars 2000    # cue JSON, bounded
yt search <url> "harness engineering"           # timestamped hits, no full read
yt search <url> "M C P" --match-mode any        # spelled acronyms collapse to mcp
yt search <url> "state of the art" --match-mode phrase
yt tracks <url>                               # list caption languages
yt info <url>                                 # title, channel, duration, views, chapters
yt channel @aiDotEngineer --limit 10          # latest uploads (exact timestamps)
yt channel @aiDotEngineer --since 2026-10-01 --json
yt doctor                                     # probe which acquisition clients work
```

Accepts `watch?v=`, `youtu.be/`, `/shorts/`, `/live/`, `/embed/` URLs or a bare
11-char video ID. Fetch details and renderer notes go to stderr so stdout stays
clean for piping.

## MCP

`yt mcp` serves stdio JSON-RPC; so does bare `yt` when stdin is not a terminal
(how MCP clients spawn a server), while bare `yt` at a prompt prints help. Call
`get_video_info` first: it reports caption availability, live status, and an
estimated transcript size, so you can decide between reading the whole
transcript, searching it, or reading a range.

| Tool | Purpose |
|---|---|
| `list_channel_videos` | Latest uploads for a channel (@handle/URL/UC…), from RSS: exact publish times, titles, links, description excerpts. |
| `get_video_info` | Metadata + caption summary + transcript size estimate + chapters. No transcript download. |
| `get_transcript` | Transcript text or cue JSON, bounded and addressable. |
| `search_transcript` | Find a topic with timestamps and context; `match_mode` all/any/phrase; zero results explain themselves. |
| `list_tracks` | Caption tracks. **Deprecated** — use `get_video_info.captions`. |

For monitoring, call `list_channel_videos` and pass the previous response's
`newest_published` as `since`. Filtering is strictly `published > since`, so no
video is reported twice. The feed exposes at most 15 entries and has no duration
or view counts; those need `get_video_info` on a specific video.

Key `get_transcript` parameters: `languages` (CSV, default `en`; `original` for
the uploader's own language), `allow_translation`, `format` (`text`|`json`),
`granularity` (`block` default |`cue`), `timestamp_mode` (`block` default
|`cue`|`none`), `start_seconds`/`end_seconds`, `start_cue`/`max_cues`,
`max_chars` (default 50000; bounds the transcript body only).

Truncation is never silent: the header carries `TRUNCATED next_start_seconds=…
next_cue_index=…`, structured metadata carries the same fields, and `next_*`
values are exact (not rounded), so resuming at them never drops the boundary
cue. Errors come back as `isError` results with a machine-readable
`error.code` (`bot_check`, `rate_limited`, `no_captions`, `bad_request`, …)
plus `retryable` and a `suggestion`.

Auto-dubbed videos list one auto-generated track per language. A non-original
track is reported as `translated=true` and keeps `original_language` at the
uploader's language, so machine-translated speech is never presented as the
speaker's words. `get_video_info.captions.original_language` names that track.

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
  YouTube, surfacing as `bot_check` or `rate_limited`. From a residential IP —
  your Mac — it just works. For a server, cross-compile
  (`GOOS=linux GOARCH=arm64 go build`) and expect to need a residential proxy
  eventually.
- Auto-generated (`asr`) captions lack punctuation, so `granularity=sentence`
  degrades to `block` with a note. Manual tracks are preferred; YouTube
  auto-translation is used when the requested language has no track, and the
  result is marked `translated=true`.
- **Multi-dub videos** expose one ASR track per language. `languages=original`
  resolves the actual original via the player's default audio track rather than
  assuming the first entry.
- Age-restricted / private videos need auth — not supported.
- `search_transcript` matches multi-word queries within one cue or across up to
  3 adjacent cues. `match_mode=any` relaxes the AND; `match_mode=phrase` requires
  adjacency. Spelled-out acronyms (`M C P`) collapse to `mcp`, and 1–2 character
  terms match whole words only. A zero result includes per-term hit counts and a
  note naming missing terms, so it is not a silent dead end. Searches cover the
  whole transcript; there is no time-range filter yet.
- `chapters` comes from the description's `Chapters:` block and is parsed from
  the full description, before the excerpt is truncated. Only lists with at least
  two strictly increasing, in-duration timestamps are returned.
- `timestamp_window_seconds` reports how much speech a timestamp covers: `30`
  for block rendering, `0` for cue-level and JSON (exact cue starts). Cite a
  block timestamp as the start of that window, not as a single line.
- `warnings` is advisory. It flags instruction-shaped caption text (e.g. "ignore
  previous instructions") so a caller can quote rather than obey. Captions remain
  `content_trust: "untrusted"` regardless.
