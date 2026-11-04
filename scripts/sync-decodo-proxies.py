#!/usr/bin/env python3
"""Sync decodo free-DC proxies from reg-platform into opencode2api's proxyfile.

reg-platform exposes them at /api/resin-proxies?type=decodo, one per line as
``scheme://user:pass@host:port#decodo``. OpenCode's free allowance is counted
per egress /24, and decodo's ten ports map onto four such ranges, so keeping one
(or a few) proxy per port covers every allowance bucket without flooding the
gateway with 900+ routes. Entries are grouped by port, one random credential is
chosen per port, and the result is written atomically.

The file is only replaced when the HTTP fetch and validation both succeed, so a
reg-platform outage cannot wipe a working proxy list. With --reload the script
sends SIGHUP to the gateway container after a change, which re-reads config.json
and, through it, this proxyfile.
"""

from __future__ import annotations

import argparse
import datetime
import hashlib
import os
import subprocess
import sys
import tempfile
import urllib.request
from collections import defaultdict
from urllib.parse import urlsplit

DEFAULT_URL = "http://127.0.0.1:18082/api/resin-proxies?type=decodo"
DEFAULT_OUTPUT = "/root/programming/github/opencode2api/runtime/proxies.decodo.txt"
DEFAULT_COMPOSE_DIR = "/root/programming/github/opencode2api"
DEFAULT_SERVICE = "opencode2api"


def log(msg: str) -> None:
    print(f"[sync-decodo-proxies] {msg}", flush=True)


def fetch(url: str, timeout: float) -> str:
    req = urllib.request.Request(url, headers={"Accept": "text/plain"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        if resp.status != 200:
            raise RuntimeError(f"HTTP {resp.status} from {url}")
        return resp.read().decode("utf-8")


def parse_lines(text: str) -> list[tuple[int, str]]:
    """Return (port, clean_url) pairs. The #decodo suffix is stripped."""
    out: list[tuple[int, str]] = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        # Cut the source marker. A '#' that starts a fragment is not part of the
        # dial URL, and opencode2api's proxyfile parser would keep it attached.
        clean = line.split("#", 1)[0].strip()
        parts = urlsplit(clean)
        if (
            parts.scheme not in ("http", "https")
            or not parts.hostname
            or not parts.port
        ):
            continue
        if not parts.username or parts.password is None:
            continue
        out.append((parts.port, clean))
    return out


def daily_seed(now: datetime.datetime | None = None) -> int:
    """Stable selection seed for one UTC day.

    A fresh random pick on every 10-minute sync would rewrite the proxyfile
    and SIGHUP the gateway each time, rebuilding the runtime and discarding the
    per-exit cool-downs the rotation logic just learned. Keeping the pick
    stable for a UTC day means reloads happen only when the pool really
    changes, and the daily rotation lines up with the free allowance reset.
    """
    day = (now or datetime.datetime.now(datetime.timezone.utc)).date().isoformat()
    return int.from_bytes(hashlib.sha256(day.encode()).digest()[:8], "big")


def select(
    entries: list[tuple[int, str]], per_port: int, seed: int | None
) -> list[str]:
    import random

    by_port: dict[int, list[str]] = defaultdict(list)
    for port, url in entries:
        by_port[port].append(url)
    rng = random.Random(seed)  # None -> system entropy, so credentials rotate
    chosen: list[str] = []
    for port in sorted(by_port):
        candidates = sorted(set(by_port[port]))
        rng.shuffle(candidates)
        chosen.extend(candidates[:per_port])
    return chosen


def atomic_write(path: str, lines: list[str]) -> bool:
    """Write lines atomically, 0644. Return True when content changed.

    0644 (not 0600) is required: the gateway container runs as the unprivileged
    opencode2api user and must be able to read the bind-mounted file. The file
    lives under /root (0700), so other host users cannot reach it anyway.
    """
    body = "\n".join(lines) + ("\n" if lines else "")
    directory = os.path.dirname(path) or "."
    os.makedirs(directory, exist_ok=True)
    try:
        with open(path, "r", encoding="utf-8") as fh:
            if fh.read() == body:
                return False
    except FileNotFoundError:
        pass
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=".proxies.", text=True)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(body)
        os.chmod(tmp, 0o644)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    return True


def reload_gateway(compose_dir: str, service: str) -> None:
    subprocess.run(
        ["docker", "compose", "kill", "-s", "HUP", service],
        cwd=compose_dir,
        check=True,
        stdout=subprocess.DEVNULL,
    )
    log(f"sent SIGHUP to {service}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--url", default=DEFAULT_URL)
    ap.add_argument("--output", default=DEFAULT_OUTPUT)
    ap.add_argument(
        "--per-port", type=int, default=1, help="proxies kept per port (default 1)"
    )
    ap.add_argument(
        "--min-ports", type=int, default=4, help="reject results with fewer ports"
    )
    ap.add_argument("--timeout", type=float, default=15.0)
    ap.add_argument("--compose-dir", default=DEFAULT_COMPOSE_DIR)
    ap.add_argument("--service", default=DEFAULT_SERVICE)
    ap.add_argument(
        "--reload", action="store_true", help="send SIGHUP when the file changed"
    )
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument(
        "--rotate-now",
        action="store_true",
        help="pick fresh credentials now instead of the stable daily seed",
    )
    args = ap.parse_args()

    try:
        text = fetch(args.url, args.timeout)
    except Exception as exc:
        log(f"ERROR: fetch failed: {exc}; leaving {args.output} untouched")
        return 1

    entries = parse_lines(text)
    ports = {port for port, _ in entries}
    if len(ports) < args.min_ports:
        log(
            f"ERROR: only {len(ports)} usable ports (want >= {args.min_ports}); leaving file untouched"
        )
        return 1

    seed = None if args.rotate_now else daily_seed()
    lines = select(entries, args.per_port, seed=seed)
    log(
        f"fetched {len(entries)} proxies across {len(ports)} ports; selected {len(lines)}"
    )

    if args.dry_run:
        for line in lines:
            print(line)
        return 0

    try:
        changed = atomic_write(args.output, lines)
    except Exception as exc:
        log(f"ERROR: write failed: {exc}")
        return 1

    if not changed:
        log("proxy list unchanged; no reload")
        return 0

    log(f"wrote {args.output}")
    if args.reload:
        try:
            reload_gateway(args.compose_dir, args.service)
        except Exception as exc:
            log(f"ERROR: reload failed: {exc}")
            return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
