#!/usr/bin/env python3
"""Exercise a disposable Margin container; use only with an empty test library."""

import argparse
import hashlib
import io
import json
import math
from pathlib import Path
import re
import sys
import time
import urllib.error
import urllib.request
import wave


TEXT = (
    "A quiet garden is a good place to listen.\n\n"
    "The morning sun warms the leaves, and a small bird sings nearby."
)
TITLE = "Container smoke test"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class Client:
    def __init__(self, base, host):
        self.base = base.rstrip("/")
        self.host = host
        # Never route this local validation through a machine's HTTP proxy.
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(self, path, *, body=None, token=None, headers=None, status=200):
        request_headers = dict(headers or {})
        if self.host:
            request_headers["Host"] = self.host
        if token:
            request_headers["X-CSRF-Token"] = token
        data = None
        if body is not None:
            request_headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data, headers=request_headers)
        with self.opener.open(request, timeout=10) as response:
            require(response.status == status, f"{path}: expected HTTP {status}, got {response.status}")
            return response.read(), response.headers

    def json(self, path, **kwargs):
        data, _ = self.request(path, **kwargs)
        return json.loads(data)


def wait_ready(client, timeout):
    deadline = time.monotonic() + timeout
    last_error = "server has not started"
    print("Waiting for first-start downloads and the speech engine...", flush=True)
    while time.monotonic() < deadline:
        try:
            config = client.json("/api/config")
            if config.get("engine", {}).get("ready"):
                require(config.get("csrf_token"), "Missing CSRF token")
                return config
            last_error = config.get("engine", {}).get("message", "engine not ready")
        except urllib.error.HTTPError as error:
            # A host/origin rejection is a configuration error, not startup delay.
            if 400 <= error.code < 500:
                raise
            last_error = str(error)
        except (urllib.error.URLError, TimeoutError, ConnectionError) as error:
            last_error = str(error)
        time.sleep(2)
    raise RuntimeError(f"Startup timed out after {timeout}s: {last_error}")


def seconds(timestamp):
    hours, minutes, secs, millis = map(int, re.split("[:,]", timestamp))
    return hours * 3600 + minutes * 60 + secs + millis / 1000


def validate_episode(client, episode):
    require(episode.get("status") == "complete", f"Episode is not complete: {episode.get('status')}")
    require(episode.get("progress") == 100, "Completed episode is not at 100 percent")
    require(episode.get("error") is None, "Completed episode has an error")
    require(episode.get("title") == TITLE and episode.get("text") == TEXT, "Stored title/text changed")
    require(episode.get("voice") == "af_heart", "Stored voice changed")
    duration = episode.get("duration", 0)
    require(math.isfinite(duration) and duration > 0, "Invalid audio duration")
    cues = episode.get("cues", [])
    require(len(cues) >= 2, "Expected measured cues for both paragraphs")
    end = 0
    for cue in cues:
        start, next_end = cue["start"], cue["end"]
        require(math.isfinite(start) and math.isfinite(next_end), "Non-finite cue timestamp")
        require(end <= start < next_end <= duration + 0.001, "Cue timestamps are out of order or outside audio")
        require(cue["text"].strip(), "Empty cue text")
        end = next_end
    require(" ".join(cue["text"] for cue in cues) == " ".join(TEXT.split()), "Cues do not cover the input")

    path = "/api/episodes/" + episode["id"]
    wav, _ = client.request(path + "/audio.wav")
    with wave.open(io.BytesIO(wav), "rb") as audio:
        require(audio.getnchannels() == 1 and audio.getsampwidth() == 2, "Expected mono PCM16 WAV")
        require(audio.getframerate() == 24000, "Unexpected sample rate")
        require(audio.getnframes() > 0, "Empty WAV")
        require(abs(audio.getnframes() / audio.getframerate() - duration) < 0.001, "WAV duration disagrees with episode")
        require(any(audio.readframes(audio.getnframes())), "WAV is entirely silent")
    partial, headers = client.request(path + "/audio.wav", headers={"Range": "bytes=0-43"}, status=206)
    require(partial == wav[:44], "Audio range request returned incorrect bytes")
    require(headers.get("Content-Range") == f"bytes 0-43/{len(wav)}", "Incorrect audio Content-Range")

    srt, _ = client.request(path + "/transcript.srt")
    txt, _ = client.request(path + "/transcript.txt")
    require(txt.decode() == TEXT, "Plain text transcript differs from the input")
    blocks = re.split(r"\r?\n\s*\r?\n", srt.decode().strip())
    require(len(blocks) == len(cues), "SRT cue count differs from the API transcript")
    for index, (block, cue) in enumerate(zip(blocks, cues), 1):
        lines = block.splitlines()
        require(len(lines) >= 3 and lines[0] == str(index), "Malformed SRT cue")
        match = re.fullmatch(r"(\d{2,}:\d{2}:\d{2},\d{3}) --> (\d{2,}:\d{2}:\d{2},\d{3})", lines[1])
        require(match is not None, "Malformed SRT timestamp")
        require(abs(seconds(match[1]) - cue["start"]) <= 0.001, "SRT start does not match measured cue")
        require(abs(seconds(match[2]) - cue["end"]) <= 0.001, "SRT end does not match measured cue")
        require(" ".join(lines[2:]) == cue["text"], "SRT text differs from measured cue")
    return {name: hashlib.sha256(data).hexdigest() for name, data in (("audio", wav), ("srt", srt), ("text", txt))}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("generate", "verify"))
    parser.add_argument("url")
    parser.add_argument("state", type=Path)
    parser.add_argument("--host-header", help="Configured Host header when Docker maps a random port")
    parser.add_argument("--timeout", type=int, default=1200, help="Startup and generation deadline in seconds")
    args = parser.parse_args()
    client = Client(args.url, args.host_header)
    config = wait_ready(client, args.timeout)

    if args.mode == "generate":
        require(client.json("/api/episodes")["episodes"] == [], "Smoke test requires an empty library")
        for path in ("/", "/app.js", "/styles.css", "/favicon.svg", "/api/voices/af_heart/preview.wav"):
            data, _ = client.request(path)
            require(data, f"Missing or empty bundled asset: {path}")
        episode = client.json("/api/episodes", body={"title": TITLE, "text": TEXT, "voice": "af_heart", "speed": 1}, token=config["csrf_token"], status=201)
        require(re.fullmatch(r"[0-9a-f]{32}", episode["id"]), "Invalid generated episode ID")
        print("Generating a short reading with the real speech engine...", flush=True)
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            episode = client.json("/api/episodes/" + episode["id"])
            if episode["status"] == "complete":
                break
            require(episode["status"] in ("queued", "generating"), f"Generation failed: {episode.get('error') or episode['status']}")
            time.sleep(1)
        else:
            raise RuntimeError("Generation timed out")
        hashes = validate_episode(client, episode)
        args.state.write_text(json.dumps({"episode": episode, "hashes": hashes}) + "\n")
        print(f"Validated {episode['duration']:.1f}s of audio, subtitles, and seeking.", flush=True)
    else:
        saved = json.loads(args.state.read_text())
        old = saved["episode"]
        episode = client.json("/api/episodes/" + old["id"])
        require(episode == old, "Episode metadata changed across container recreation")
        require(validate_episode(client, episode) == saved["hashes"], "Generated files changed across container recreation")
        require([item["id"] for item in client.json("/api/episodes")["episodes"]] == [old["id"]], "Persisted library does not contain the expected reading")
        print("Library, audio, and transcripts survived container recreation unchanged.", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, TypeError, wave.Error) as error:
        print(f"Smoke test failed: {error}", file=sys.stderr)
        sys.exit(1)
