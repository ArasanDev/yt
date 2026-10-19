"""Fetch YouTube transcripts.

Fast path: YouTube's own caption tracks via youtube-transcript-api (free, no
API key). Fallback: download audio with yt-dlp and transcribe locally with
faster-whisper (free, runs on this machine).
"""

from __future__ import annotations

import contextlib
import json
import os
import re
import subprocess
import sys
import tempfile
from dataclasses import asdict, dataclass, field
from pathlib import Path

try:
    from youtube_transcript_api._errors import CouldNotRetrieveTranscript
except ImportError:  # older releases export from the top level
    from youtube_transcript_api import CouldNotRetrieveTranscript

_VIDEO_ID_RE = re.compile(
    r"(?:youtube\.com/(?:watch\?[^#\s]*v=|shorts/|live/|embed/|v/)|youtu\.be/)"
    r"([\w-]{11})"
)
_BARE_ID_RE = re.compile(r"^[\w-]{11}$")

# list()/find_transcript() failures that mean "no captions exist" — worth
# falling back to Whisper. Anything else (IpBlocked, VideoUnavailable, ...)
# propagates as a hard error.
_NO_CAPTIONS_ERRORS = {"TranscriptsDisabled", "NoTranscriptFound"}


class TranscriptError(Exception):
    """Raised when a transcript cannot be fetched."""


class _NoCaptions(Exception):
    """Internal: no usable caption track; try the whisper fallback."""


@dataclass
class Segment:
    start: float
    duration: float
    text: str


@dataclass
class Transcript:
    video_id: str
    source: str  # "captions" | "whisper"
    language: str
    generated: bool
    segments: list[Segment] = field(default_factory=list)

    @property
    def text(self) -> str:
        return " ".join(s.text for s in self.segments)


def extract_video_id(url_or_id: str) -> str:
    s = url_or_id.strip()
    if _BARE_ID_RE.match(s):
        return s
    m = _VIDEO_ID_RE.search(s)
    if m:
        return m.group(1)
    raise ValueError(f"could not extract a YouTube video id from {url_or_id!r}")


def _ytt_api():
    from youtube_transcript_api import YouTubeTranscriptApi

    user = os.environ.get("WEBSHARE_PROXY_USERNAME")
    password = os.environ.get("WEBSHARE_PROXY_PASSWORD")
    if user and password:
        from youtube_transcript_api.proxies import WebshareProxyConfig

        return YouTubeTranscriptApi(
            proxy_config=WebshareProxyConfig(
                proxy_username=user, proxy_password=password
            )
        )
    return YouTubeTranscriptApi()


def fetch_transcript(
    url_or_id: str,
    languages: tuple[str, ...] = ("en",),
    whisper: bool = True,
    whisper_model: str | None = None,
) -> Transcript:
    video_id = extract_video_id(url_or_id)
    try:
        return _fetch_captions(video_id, languages)
    except _NoCaptions as e:
        if not whisper:
            raise TranscriptError(
                f"no captions for {video_id} "
                f"(languages tried: {', '.join(languages)}) and the whisper "
                "fallback is disabled"
            ) from e
        return _fetch_whisper(
            video_id,
            whisper_model or os.environ.get("YT_WHISPER_MODEL", "base"),
            languages,
        )


def _fetch_captions(video_id: str, languages: tuple[str, ...]) -> Transcript:
    api = _ytt_api()
    try:
        tlist = api.list(video_id)
    except CouldNotRetrieveTranscript as e:
        if type(e).__name__ in _NO_CAPTIONS_ERRORS:
            raise _NoCaptions(str(e)) from e
        raise TranscriptError(str(e)) from e

    try:
        track = tlist.find_transcript(languages)
    except CouldNotRetrieveTranscript:
        # No track in a requested language — translate any available track.
        track = None
        for candidate in tlist:
            try:
                track = candidate.translate(languages[0])
                break
            except CouldNotRetrieveTranscript:
                continue
        if track is None:
            raise _NoCaptions(f"no usable caption track for {video_id}")

    segments = [_segment(s) for s in track.fetch()]
    if not segments:
        raise _NoCaptions(f"caption track for {video_id} was empty")
    return Transcript(
        video_id=video_id,
        source="captions",
        language=track.language_code,
        generated=track.is_generated,
        segments=segments,
    )


def _segment(snippet) -> Segment:
    if isinstance(snippet, dict):
        return Segment(snippet["start"], snippet["duration"], snippet["text"])
    return Segment(snippet.start, snippet.duration, snippet.text)


def _fetch_whisper(
    video_id: str, model_size: str, languages: tuple[str, ...]
) -> Transcript:
    try:
        from faster_whisper import WhisperModel
    except ImportError as e:
        raise TranscriptError(
            "no captions available and the whisper fallback is not installed "
            "— run `uv sync --extra whisper` in the yt-transcript project"
        ) from e
    import yt_dlp

    watch_url = f"https://www.youtube.com/watch?v={video_id}"
    # stdout is the JSON-RPC channel when running as an MCP stdio server —
    # yt-dlp/hf progress bars would corrupt it, so send everything to stderr.
    with tempfile.TemporaryDirectory(prefix="yt-transcript-") as td, (
        contextlib.redirect_stdout(sys.stderr)
    ):
        opts = {
            "format": "bestaudio[ext=m4a]/bestaudio/best",
            "outtmpl": os.path.join(td, "audio.%(ext)s"),
            "quiet": True,
            "no_warnings": True,
            "noplaylist": True,
            "noprogress": True,
        }
        try:
            with yt_dlp.YoutubeDL(opts) as ydl:
                ydl.download([watch_url])
        except Exception as e:
            raise TranscriptError(f"audio download failed: {e}") from e
        audio = next(iter(Path(td).glob("audio.*")), None)
        if audio is None:
            raise TranscriptError("audio download produced no file")

        model = WhisperModel(model_size, device="auto", compute_type="default")
        seg_iter, info = model.transcribe(
            _load_audio(audio), language=languages[0] if languages else None
        )
        segments = [
            Segment(start=s.start, duration=s.end - s.start, text=s.text.strip())
            for s in seg_iter
        ]
    if not segments:
        raise TranscriptError("whisper produced an empty transcript")
    return Transcript(
        video_id=video_id,
        source="whisper",
        language=info.language,
        generated=True,
        segments=segments,
    )


def _load_audio(path: Path):
    """Decode audio to 16 kHz mono float32 via ffmpeg.

    faster-whisper's own decoder goes through PyAV, whose API drifts across
    releases; ffmpeg is a hard requirement for yt-dlp anyway, so use it
    directly and pass a numpy array to transcribe() instead.
    """
    import numpy as np

    proc = subprocess.run(
        [
            "ffmpeg", "-i", str(path),
            "-f", "s16le", "-ac", "1", "-ar", "16000",
            "-loglevel", "error", "-",
        ],
        capture_output=True,
    )
    if proc.returncode != 0:
        raise TranscriptError(
            f"ffmpeg audio decode failed: {proc.stderr.decode(errors='replace').strip()}"
        )
    return np.frombuffer(proc.stdout, dtype=np.int16).astype(np.float32) / 32768.0


def video_metadata(url_or_id: str) -> dict:
    """Title/channel/duration/etc. without downloading the video."""
    import yt_dlp

    video_id = extract_video_id(url_or_id)
    opts = {"quiet": True, "no_warnings": True, "noplaylist": True}
    try:
        with yt_dlp.YoutubeDL(opts) as ydl:
            info = ydl.extract_info(
                f"https://www.youtube.com/watch?v={video_id}", download=False
            )
    except Exception as e:
        raise TranscriptError(f"metadata fetch failed: {e}") from e
    keys = (
        "id", "title", "uploader", "channel", "channel_id", "duration",
        "upload_date", "view_count", "like_count", "webpage_url",
    )
    out = {k: info.get(k) for k in keys}
    desc = info.get("description") or ""
    out["description"] = desc[:500] + ("…" if len(desc) > 500 else "")
    return out


def _ts(seconds: float) -> str:
    s = int(seconds)
    h, rem = divmod(s, 3600)
    m, sec = divmod(rem, 60)
    return f"{h:02d}:{m:02d}:{sec:02d}" if h else f"{m:02d}:{sec:02d}"


def _srt_ts(seconds: float) -> str:
    ms = int(round(seconds * 1000))
    h, rem = divmod(ms, 3_600_000)
    m, rem = divmod(rem, 60_000)
    s, ms = divmod(rem, 1000)
    return f"{h:02d}:{m:02d}:{s:02d},{ms:03d}"


def format_text(t: Transcript, timestamps: bool = False) -> str:
    if not timestamps:
        return t.text
    return "\n".join(f"[{_ts(s.start)}] {s.text}" for s in t.segments)


def format_srt(t: Transcript) -> str:
    blocks = [
        f"{i}\n{_srt_ts(s.start)} --> {_srt_ts(s.start + s.duration)}\n{s.text}"
        for i, s in enumerate(t.segments, 1)
    ]
    return "\n\n".join(blocks) + "\n"


def format_json(t: Transcript) -> str:
    return json.dumps(asdict(t), indent=2, ensure_ascii=False)
