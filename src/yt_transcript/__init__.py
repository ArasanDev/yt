"""YouTube transcripts for agents: captions fast-path, local Whisper fallback."""

from .core import (
    Segment,
    Transcript,
    TranscriptError,
    extract_video_id,
    fetch_transcript,
    format_json,
    format_srt,
    format_text,
    video_metadata,
)

__all__ = [
    "Segment",
    "Transcript",
    "TranscriptError",
    "extract_video_id",
    "fetch_transcript",
    "format_json",
    "format_srt",
    "format_text",
    "video_metadata",
]
