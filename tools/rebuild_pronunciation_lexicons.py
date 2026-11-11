#!/usr/bin/env python3
"""Rebuild only the permissive English lexicons and Kokoro token table.

Download the pinned archive URL listed in assets/pronunciation/PROVENANCE.json,
then run this developer tool with --archive FILE --output DIRECTORY. Nothing
else from that archive is extracted, including its obsolete eSpeak data.
The input archive, each uncompressed file, and each output file are SHA-256
verified. Gzip uses level 9, mtime 0, no filename, and OS byte 255.
"""

import argparse
import gzip
import hashlib
import io
from pathlib import Path
import tarfile


ARCHIVE_SHA256 = "60703707bc05ccd48d60e1eb723f38b07d6e474795a0b6a588d7220400e0f7a4"
FILES = {
    "lexicon-us-en.txt": (
        "7daaab53a181be9885b853a8582bf1838186317e5dadacbcef9c426d6fa0da14",
        "2d816cfc5494d2ce170418a5921a952e08759d88bbc9f9ddfa9afcadb9df3f11",
    ),
    "lexicon-gb-en.txt": (
        "c4cbb37316f62210dff52718a7afcaae24f50c032cc75ab47ae67b831d1049e7",
        "23d793c877715fda4668219b1206eaa3a50a6d30a703f46cea2a830a72ec5819",
    ),
    "tokens.txt": (
        "6ebb6bb288f20f3ae8d004d3c2ca27697da27c037d75e81a60e2a6a663f95425",
        "6ebb6bb288f20f3ae8d004d3c2ca27697da27c037d75e81a60e2a6a663f95425",
    ),
}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def rebuild(archive: Path, output: Path):
    with archive.open("rb") as source:
        archive_hash = hashlib.file_digest(source, "sha256").hexdigest()
    if archive_hash != ARCHIVE_SHA256:
        raise ValueError("Archive failed SHA-256 verification")
    wanted = {f"kokoro-multi-lang-v1_0/{name}": name for name in FILES}
    extracted = {}
    with tarfile.open(archive, "r|bz2") as source:
        for member in source:
            if member.name not in wanted:
                continue
            name = wanted[member.name]
            if not member.isfile() or member.size > 8_000_000:
                raise ValueError(f"Invalid archive entry: {name}")
            raw = source.extractfile(member).read()
            if digest(raw) != FILES[name][0]:
                raise ValueError(f"Source file failed SHA-256 verification: {name}")
            if name.startswith("lexicon-"):
                buffer = io.BytesIO()
                with gzip.GzipFile(filename="", mode="wb", fileobj=buffer, compresslevel=9, mtime=0) as compressed:
                    compressed.write(raw)
                data = buffer.getvalue()
                destination = name + ".gz"
            else:
                data, destination = raw, name
            if digest(data) != FILES[name][1]:
                raise ValueError(f"Output hash differs: {destination}; check the documented gzip settings and zlib version")
            extracted[destination] = data
            if len(extracted) == len(FILES):
                break
    if len(extracted) != len(FILES):
        raise ValueError("Archive is missing a required pronunciation file")
    output.mkdir(parents=True, exist_ok=True)
    for name, data in extracted.items():
        (output / name).write_bytes(data)
        print(f"{name}: {digest(data)}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    rebuild(args.archive, args.output)
