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
(`.git`, `.devin`, `.github`). Everything else lives here.

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
  internal/version/  build version (overridable with -ldflags)
```

CI lives at the repository root in `.github/workflows/ci.yml` and runs
`gofmt`, `go vet`, `go test -race`, and `go build` from `yt/`.

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

Release builds can stamp a version (the default is `dev`):

```sh
go build -ldflags "-X yt/internal/version.Version=vX.Y.Z" -o bin/yt ./cmd/yt
yt --version
```

## CLI

```sh
yt transcript <url-or-id>                       # transcript to stdout
yt transcript <url> -l en,es -t                 # language fallback + [MM:SS] stamps
yt transcript <url> --start 1274 --end 1290     # time range
yt transcript <url> -f srt -o subs.srt          # SRT file
yt transcript <url> -f json --max-chars 2000    # cue JSON, bounded
yt transcript <url> --bare                      # body only, no metadata header
yt transcript <url> --start-cue 120 --max-cues 40  # resumable cue window
yt search <url> "harness engineering"           # timestamped hits, no full read
yt search <url> "M C P" --match-mode any        # spelled acronyms collapse to mcp
yt search <url> "state of the art" --match-mode phrase
yt search <url> "harness" --context-cues 2      # human output shows context
yt tracks <url>                               # list caption languages
yt info <url>                                 # title, channel, duration, views, chapters
yt channel @aiDotEngineer --limit 10          # latest uploads (exact timestamps)
yt channel @aiDotEngineer --since 2026-10-01 --json
yt doctor                                     # probe which acquisition clients work
yt --version                                  # build version (dev unless stamped)
```

Accepts `watch?v=`, `youtu.be/`, `/shorts/`, `/live/`, `/embed/` URLs or a bare
11-char video ID. By default, `-f text` includes a metadata header and, when
needed, a truncation trailer. Use `--bare` for body-only stdout; truncation is
then reported on stderr. Fetch/renderer notes always go to stderr.

CLI usage errors and upstream failures are typed. `cmd/yt` prints `code`,
`retryable`, `retry_after_seconds`, and `suggestion` to stderr while keeping
stdout clean. Invalid formats, negative limits, and inverted time ranges fail
before any network request.

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
cue. `max_cues` clipping is reported even when `end_seconds` is also set.
Errors come back as `isError` results with a machine-readable `error.code`
(`bot_check`, `rate_limited`, `no_captions`, `bad_request`, …) plus
`retryable` and a `suggestion`.

The transcript body is returned in both `content` and `structuredContent` for
client compatibility, so a response is roughly twice the body size;
`returned_chars` measures one copy.

Auto-dubbed videos list one auto-generated track per language. A track whose
language differs from the uploader's known original is reported as
`translated=true`, whether it is manual or ASR, so machine-translated speech is
never presented as the speaker's words. `get_video_info.captions.original_language`
names that track. If the player response carries no audio-track metadata and
multiple caption languages exist, the original is unknown: `languages=original`
returns an actionable `no_matching_language` error instead of guessing, and
`original_language` may be empty. Request a specific language in that case.

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
  resolves the actual original via YouTube's audio-track→caption mapping; if
  that metadata is absent, the original is unknown and the tool fails with an
  actionable error rather than guessing.
- Age-restricted / private videos need auth — not supported.
- `search_transcript` matches multi-word queries within one cue or across up to
  3 adjacent cues. `match_mode=any` relaxes the AND; `match_mode=phrase` requires
  adjacency. Contiguous runs of three or more single-letter tokens (`M C P`)
  collapse to `mcp`, and terms of 3 or fewer characters match whole words only.
  A zero result includes per-term hit counts and a note naming missing terms or
  a start-cue boundary, so it is not a silent dead end. Searches cover the
  whole transcript; there is no time-range filter yet.
- `chapters` comes from the description's `Chapters:` block and is parsed from
  the full description, before the excerpt is truncated. Emoji/markdown
  headings and an inline first entry are accepted; invalid, duplicate,
  out-of-order, or past-duration entries are dropped. At least two valid,
  strictly increasing, in-duration timestamps are required.
- `timestamp_window_seconds` follows the timestamp mode: `30` when timestamps
  are block-level, `0` for cue-level or disabled timestamps. Cite a block
  timestamp as the start of that window, not as a single line.
- `warnings` is advisory. It flags instruction-shaped text in transcripts,
  search matches/context, video metadata, and channel listings so a caller can
  quote rather than obey. User-generated content remains
  `content_trust: "untrusted"` regardless.
