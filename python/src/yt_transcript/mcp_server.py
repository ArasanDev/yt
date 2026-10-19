"""MCP server: expose YouTube transcripts to agents over stdio.

Run:  yt-mcp          (stdio transport)
"""

from __future__ import annotations

import json

try:
    from mcp.server.mcpserver import MCPServer  # mcp >= 2.x
except ImportError:
    from mcp.server.fastmcp import FastMCP as MCPServer  # mcp 1.x

from .core import (
    TranscriptError,
    fetch_transcript,
    format_text,
    video_metadata,
)

mcp = MCPServer("yt-transcript")


@mcp.tool()
def get_transcript(
    url_or_id: str,
    languages: str = "en",
    timestamps: bool = False,
    whisper_fallback: bool = True,
) -> str:
    """Fetch the spoken transcript of a YouTube video.

    Uses the video's own captions when they exist; falls back to local Whisper
    speech-to-text (free, runs on this machine) for videos without captions.

    Args:
        url_or_id: YouTube URL (watch, youtu.be, shorts, live, embed) or a bare
            11-character video ID.
        languages: comma-separated ISO codes tried in order, e.g. "en" or
            "en,es". Auto-translates into the first language if needed.
        timestamps: prefix each line with [MM:SS] so you can cite positions.
        whisper_fallback: set false to only use existing captions (fast fail
            when a video has none).
    """
    langs = tuple(x.strip() for x in languages.split(",") if x.strip()) or ("en",)
    try:
        t = fetch_transcript(url_or_id, langs, whisper=whisper_fallback)
    except (TranscriptError, ValueError) as e:
        return f"error: {e}"
    header = (
        f"# transcript | video={t.video_id} source={t.source} "
        f"language={t.language} auto_generated={t.generated}"
    )
    return f"{header}\n\n{format_text(t, timestamps=timestamps)}"


@mcp.tool()
def get_video_info(url_or_id: str) -> str:
    """Get metadata for a YouTube video without downloading it.

    Returns JSON with title, channel, duration (seconds), upload date, view
    count, and the first 500 chars of the description. Useful for triaging a
    video before fetching its transcript.
    """
    try:
        return json.dumps(video_metadata(url_or_id), indent=2, ensure_ascii=False)
    except (TranscriptError, ValueError) as e:
        return f"error: {e}"


def main() -> None:
    mcp.run()


if __name__ == "__main__":
    main()
