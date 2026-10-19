"""CLI: yt-transcript <url-or-id> [--lang en,es] [--timestamps] [--format srt]"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .core import (
    TranscriptError,
    fetch_transcript,
    format_json,
    format_srt,
    format_text,
    video_metadata,
)


def main(argv: list[str] | None = None) -> None:
    p = argparse.ArgumentParser(
        prog="yt-transcript",
        description=(
            "Fetch a YouTube transcript. Uses the video's captions when "
            "available; falls back to local Whisper speech-to-text."
        ),
    )
    p.add_argument("url", help="YouTube URL (watch/youtu.be/shorts/live) or 11-char video ID")
    p.add_argument("-l", "--lang", default="en",
                   help="comma-separated language fallbacks, e.g. 'en,es' (default: en)")
    p.add_argument("-t", "--timestamps", action="store_true",
                   help="prefix each segment with [MM:SS]")
    p.add_argument("-f", "--format", choices=("text", "json", "srt"), default="text")
    p.add_argument("--no-whisper", action="store_true",
                   help="disable the Whisper fallback (captions only)")
    p.add_argument("--whisper-model", default=None,
                   help="faster-whisper model size (default: base, or $YT_WHISPER_MODEL)")
    p.add_argument("--info", action="store_true",
                   help="print video metadata (title, channel, duration) instead of the transcript")
    p.add_argument("-o", "--output", help="write to a file instead of stdout")
    args = p.parse_args(argv)

    langs = tuple(x.strip() for x in args.lang.split(",") if x.strip()) or ("en",)
    try:
        if args.info:
            out = json.dumps(video_metadata(args.url), indent=2, ensure_ascii=False)
        else:
            t = fetch_transcript(
                args.url,
                langs,
                whisper=not args.no_whisper,
                whisper_model=args.whisper_model,
            )
            out = {
                "text": lambda: format_text(t, args.timestamps),
                "json": lambda: format_json(t),
                "srt": lambda: format_srt(t),
            }[args.format]()
    except TranscriptError as e:
        print(f"error: {e}", file=sys.stderr)
        sys.exit(1)
    except ValueError as e:
        print(f"error: {e}", file=sys.stderr)
        sys.exit(2)

    if args.output:
        Path(args.output).write_text(out, encoding="utf-8")
    else:
        print(out)


if __name__ == "__main__":
    main()
