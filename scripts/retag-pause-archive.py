#!/usr/bin/env python3
"""Retag a crane image archive as portainer/pause:latest.

crane pull of a digest reference records that digest as the archive name.
containerd imports RepoTags, and the CRI sandbox ref stays portainer/pause:latest.
The layer bytes are unchanged.
"""

import io
import json
import os
import sys
import tarfile

TAGS = ["docker.io/portainer/pause:latest", "portainer/pause:latest"]
REF_NAME = "docker.io/portainer/pause:latest"


def main() -> None:
    if len(sys.argv) != 2:
        sys.exit("usage: retag-pause-archive.py ARCHIVE.tar")
    path = sys.argv[1]
    dest = path + ".retag"
    with tarfile.open(path, "r") as src:
        names = set(src.getnames())
        if "manifest.json" in names:
            retag_docker(src, dest)
        elif "index.json" in names:
            retag_oci(src, dest)
        else:
            shown = ", ".join(sorted(names)[:20])
            sys.exit(f"unrecognized image archive {path}: {shown}")
    os.replace(dest, path)


def retag_docker(src: tarfile.TarFile, dest: str) -> None:
    raw = src.extractfile("manifest.json")
    if raw is None:
        sys.exit("manifest.json is empty")
    manifest = json.loads(raw.read())
    if not isinstance(manifest, list) or not manifest:
        sys.exit("manifest.json has no images")
    for item in manifest:
        item["RepoTags"] = TAGS
    data = json.dumps(manifest, indent=2).encode() + b"\n"
    with tarfile.open(dest, "w") as out:
        for member in src.getmembers():
            if member.name == "manifest.json":
                info = tarfile.TarInfo(name="manifest.json")
                info.size = len(data)
                info.mtime = 0
                info.mode = member.mode
                out.addfile(info, io.BytesIO(data))
                continue
            fileobj = src.extractfile(member) if member.isfile() else None
            out.addfile(member, fileobj)


def retag_oci(src: tarfile.TarFile, dest: str) -> None:
    raw = src.extractfile("index.json")
    if raw is None:
        sys.exit("index.json is empty")
    index = json.loads(raw.read())
    manifests = index.get("manifests")
    if not isinstance(manifests, list) or not manifests:
        sys.exit("index.json has no manifests")
    for item in manifests:
        annotations = item.get("annotations")
        if not isinstance(annotations, dict):
            annotations = {}
            item["annotations"] = annotations
        annotations["org.opencontainers.image.ref.name"] = REF_NAME
    data = json.dumps(index, indent=2).encode() + b"\n"
    with tarfile.open(dest, "w") as out:
        for member in src.getmembers():
            if member.name == "index.json":
                info = tarfile.TarInfo(name="index.json")
                info.size = len(data)
                info.mtime = 0
                info.mode = member.mode
                out.addfile(info, io.BytesIO(data))
                continue
            fileobj = src.extractfile(member) if member.isfile() else None
            out.addfile(member, fileobj)


if __name__ == "__main__":
    main()
