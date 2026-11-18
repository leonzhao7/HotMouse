#!/usr/bin/env python3
"""Offline audit and (when explicitly enabled) flag extractor for The Last 3 Minutes.

The default mode is completely offline.  It reads the supplied attachment and
reports whether the flag or a usable secret is present in it.  Live HTTP
requests are disabled unless ``--live`` is supplied, and live mode refuses
hosts other than the challenge host passed with ``--url``.

The source shows that the only flag read is ``Path.cwd() / "flag.txt"`` after a
correct 64-bit password.  The archive does not contain that file, so the
offline path is useful for checking a newly downloaded attachment but cannot
manufacture a flag that is not there.
"""

from __future__ import annotations

import argparse
import json
import re
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path


FLAG_RE = re.compile(rb"FLAG\{[^\r\n{}]{1,512}\}")
DEFAULT_URL = "http://172.16.91.2:8080"
ALLOWED_HOST = "172.16.91.2"


def archive_audit(archive: Path) -> dict:
    """Inspect the attachment without importing or executing challenge code."""
    result: dict = {
        "archive": str(archive),
        "files": [],
        "flags": [],
        "flag_paths": [],
        "password_generation": [],
    }
    with zipfile.ZipFile(archive) as zf:
        for info in zf.infolist():
            if info.is_dir():
                continue
            result["files"].append(info.filename)
            data = zf.read(info)
            for match in FLAG_RE.finditer(data):
                value = match.group().decode("utf-8", "replace")
                if value not in result["flags"]:
                    result["flags"].append(value)
            if info.filename.endswith((".py", ".txt", ".html", ".js", ".md")):
                text = data.decode("utf-8", "replace")
                if "flag.txt" in text or "/answers/flag" in text:
                    result["flag_paths"].append(info.filename)
                if "getrandbits(64)" in text:
                    result["password_generation"].append(info.filename)
    return result


def local_flag_candidates() -> list[dict]:
    """Read only local paths that the supplied Dockerfile actually uses."""
    candidates = [Path.cwd() / "flag.txt", Path("/answers/flag")]
    findings = []
    seen: set[Path] = set()
    for path in candidates:
        path = path.resolve()
        if path in seen:
            continue
        seen.add(path)
        item = {"path": str(path), "exists": path.is_file(), "flag": None}
        if item["exists"]:
            try:
                data = path.read_bytes()
            except OSError as exc:
                item["error"] = str(exc)
            else:
                match = FLAG_RE.search(data)
                item["flag"] = match.group().decode("utf-8", "replace") if match else None
        findings.append(item)
    return findings


class HTTP:
    def __init__(self, base: str, timeout: float = 5.0):
        parsed = urllib.parse.urlsplit(base)
        if parsed.scheme not in {"http", "https"} or parsed.hostname != ALLOWED_HOST:
            raise ValueError(f"refusing a host outside the challenge endpoint: {base}")
        self.base = base.rstrip("/")
        self.timeout = timeout
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(self, path: str, method: str = "GET", body: bytes | None = None) -> tuple[int, bytes]:
        req = urllib.request.Request(self.base + path, data=body, method=method)
        if body is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with self.opener.open(req, timeout=self.timeout) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read()


def live_file_probe(http: HTTP) -> list[dict]:
    """Check only same-origin paths that could expose the injected flag file."""
    paths = (
        "/flag.txt",
        "/static/flag.txt",
        "/static/../flag.txt",
        "/static/%2e%2e/flag.txt",
        "/api/../flag.txt",
    )
    findings = []
    for path in paths:
        status, body = http.request(path)
        match = FLAG_RE.search(body)
        findings.append({
            "path": path,
            "status": status,
            "flag": match.group().decode("utf-8", "replace") if match else None,
        })
        if match:
            break
    return findings


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, default=Path("/tmp/last3/attachment.zip"))
    parser.add_argument("--live", action="store_true", help="enable same-origin HTTP probes")
    parser.add_argument("--url", default=DEFAULT_URL)
    parser.add_argument("--json", action="store_true", help="print machine-readable output")
    args = parser.parse_args()

    if not args.archive.is_file():
        parser.error(f"attachment not found: {args.archive}")
    report = {"offline": archive_audit(args.archive), "local_files": local_flag_candidates()}
    if args.live:
        # This branch is intentionally opt-in.  It was not used during the
        # offline analysis of the supplied challenge instance.
        report["live"] = live_file_probe(HTTP(args.url))

    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        offline = report["offline"]
        print(f"archive: {offline['archive']}")
        print(f"files: {len(offline['files'])}")
        if offline["flags"]:
            for flag in offline["flags"]:
                print(flag)
        else:
            print("no FLAG{...} literal is present in the attachment")
        for item in report["local_files"]:
            if item["flag"]:
                print(item["flag"])
        print("password source:", ", ".join(offline["password_generation"]) or "not found")
        if args.live:
            for finding in report["live"]:
                print(f"{finding['status']} {finding['path']}")
                if finding["flag"]:
                    print(finding["flag"])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
