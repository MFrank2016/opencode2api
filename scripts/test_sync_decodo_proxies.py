#!/usr/bin/env python3
"""Tests for sync-decodo-proxies.py. Run: python3 -m unittest discover -s scripts -p 'test_*.py' -v"""

import importlib.util
import os
import sys
import tempfile
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "sync_decodo_proxies", Path(__file__).resolve().parent / "sync-decodo-proxies.py"
)
sync = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sync)


class ParseLinesTests(unittest.TestCase):
    def test_parses_valid_lines_and_strips_source_marker(self):
        text = (
            "http://u1:p1@dc.decodo.com:10001#decodo\n"
            "http://u2:p2@dc.decodo.com:10002#decodo\n"
        )
        self.assertEqual(
            sync.parse_lines(text),
            [
                (10001, "http://u1:p1@dc.decodo.com:10001"),
                (10002, "http://u2:p2@dc.decodo.com:10002"),
            ],
        )

    def test_skips_malformed_and_credentialless_lines(self):
        text = "\n".join(
            [
                "# comment",
                "not a proxy",
                "http://dc.decodo.com:10003#decodo",  # no credentials
                "http://u:p@dc.decodo.com#decodo",  # no port
                "ftp://u:p@dc.decodo.com:10004#decodo",  # bad scheme
                "http://u:p@dc.decodo.com:10005#decodo",  # valid
            ]
        )
        self.assertEqual(
            sync.parse_lines(text), [(10005, "http://u:p@dc.decodo.com:10005")]
        )


class SelectTests(unittest.TestCase):
    def test_keeps_one_random_proxy_per_port(self):
        entries = []
        for port in (10001, 10002, 10003):
            for i in range(5):
                entries.append((port, f"http://u{i}:p@dc.decodo.com:{port}"))
        chosen = sync.select(entries, per_port=1, seed=42)
        self.assertEqual(len(chosen), 3)
        ports = {int(url.rsplit(":", 1)[1]) for url in chosen}
        self.assertEqual(ports, {10001, 10002, 10003})

    def test_seed_is_reproducible(self):
        entries = [(10001, f"http://u{i}:p@dc.decodo.com:10001") for i in range(5)]
        self.assertEqual(
            sync.select(entries, 1, seed=7),
            sync.select(entries, 1, seed=7),
        )

    def test_per_port_two_keeps_two_distinct_per_port(self):
        entries = [(10001, f"http://u{i}:p@dc.decodo.com:10001") for i in range(5)]
        chosen = sync.select(entries, 2, seed=1)
        self.assertEqual(len(chosen), 2)
        self.assertEqual(len(set(chosen)), 2)


class AtomicWriteTests(unittest.TestCase):
    def test_writes_0600_and_reports_no_change_on_identical_content(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "proxies.txt")
            lines = ["http://u:p@dc.decodo.com:10001"]
            self.assertTrue(sync.atomic_write(path, lines))
            self.assertEqual(oct(os.stat(path).st_mode & 0o777), "0o600")
            self.assertFalse(sync.atomic_write(path, lines))
            with open(path, encoding="utf-8") as fh:
                self.assertEqual(fh.read(), lines[0] + "\n")

    def test_replaces_content_when_changed(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "proxies.txt")
            sync.atomic_write(path, ["http://u:p@dc.decodo.com:10001"])
            self.assertTrue(sync.atomic_write(path, ["http://u:p@dc.decodo.com:10002"]))
            with open(path, encoding="utf-8") as fh:
                self.assertIn("10002", fh.read())


if __name__ == "__main__":
    unittest.main()
