# yt-transcript

YouTube transcripts for agents. Free, no API keys, runs entirely locally.

- **Fast path** — the video's own caption track (manual or auto-generated) via
  `youtube-transcript-api`. No download, sub-second.
- **Fallback** — no captions? Download the audio with `yt-dlp` and transcribe
  locally with `faster-whisper`. Optional extra, still free.

Two front-ends over the same core:

- `yt-transcript` — CLI for humans and scripts.
- `yt-mcp` — MCP stdio server so agents get `get_transcript` / `get_video_info`
  tools.

## Install

```sh
uv sync                  # captions path only
uv sync --extra whisper  # + local speech-to-text fallback
```

## CLI

```sh
yt-transcript "https://www.youtube.com/watch?v=jNQXAC9IVRw"
yt-transcript jNQXAC9IVRw --timestamps
yt-transcript <url> --lang en,es --format srt -o subs.srt
yt-transcript <url> --info               # title, channel, duration, views
yt-transcript <url> --whisper-model small # better quality, slower
```

## MCP tools

| Tool | Returns |
|---|---|
| `get_transcript(url_or_id, languages, timestamps, whisper_fallback)` | Transcript text; header notes `source=captions|whisper` |
| `get_video_info(url_or_id)` | JSON: title, channel, duration, views, description excerpt |

### Wire it into agents

The server binary lands at `.venv/bin/yt-mcp` after `uv sync`.

```sh
# Devin CLI (user-wide)
devin mcp add yt-transcript -- /path/to/yt/.venv/bin/yt-mcp

# Claude Code
claude mcp add yt-transcript -- /path/to/yt/.venv/bin/yt-mcp
```

Or raw `mcpServers` JSON (works for Devin `~/.config/devin/mcp_config.json`,
Claude Desktop, Cursor, Windsurf, etc.):

```json
{
  "mcpServers": {
    "yt-transcript": {
      "command": "/path/to/yt/.venv/bin/yt-mcp"
    }
  }
}
```

## Caveats

- **IP blocking**: YouTube blocks caption requests from datacenter IPs (AWS,
  GCP, Hetzner, most VPSes). From a residential IP — your Mac — it just works.
  On a server, set `WEBSHARE_PROXY_USERNAME` / `WEBSHARE_PROXY_PASSWORD` for
  rotating residential proxies, or run the MCP server locally.
- **Auto-captions** have no punctuation and occasional errors — fine for agent
  consumption. Manual captions are preferred automatically.
- **Whisper fallback** requires `ffmpeg` on PATH (`brew install ffmpeg`),
  downloads a model on first use (`base` ≈150 MB; set `YT_WHISPER_MODEL` for
  `tiny`/`small`/…), and is much slower than captions — roughly real-time for
  `base` on Apple Silicon.
- Age-restricted/private videos need cookies: `yt-dlp` respects
  `--cookies-from-browser` — not yet wired into this tool.
