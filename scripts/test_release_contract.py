#!/usr/bin/env python3
"""Tests for the Makefile's release targets and the scripts/ they call.

Run with `python3 -I scripts/test_release_contract.py`. None of it needs Go:
the module-path guard, the module-info helper and the proxy poll are plain
shell, so a local HTTP server stands in for proxy.golang.org.
"""

import http.server
import os
import subprocess
import tempfile
import threading
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def run(cmd, env=None, **kw):
    full = {**os.environ, **(env or {})}
    return subprocess.run(
        cmd, cwd=ROOT, env=full, capture_output=True, text=True, check=False, **kw
    )


class WithGoMod(unittest.TestCase):
    def go_mod(self, module):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        path = Path(tmp.name) / "go.mod"
        path.write_text(f"module {module}\n\ngo 1.26.0\n")
        return {"GO_MOD": str(path)}


class ModuleInfo(WithGoMod):
    def info(self, module, what):
        r = run(["scripts/module-info.sh", what], env=self.go_mod(module))
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout.strip()

    def test_plain_path(self):
        self.assertEqual(self.info("github.com/acme/lib", "path"), "github.com/acme/lib")
        self.assertEqual(self.info("github.com/acme/lib", "name"), "lib")
        self.assertEqual(self.info("github.com/acme/lib", "major"), "")

    def test_major_suffix(self):
        self.assertEqual(self.info("github.com/acme/lib/v12", "name"), "lib")
        self.assertEqual(self.info("github.com/acme/lib/v12", "major"), "12")

    def test_name_starting_with_v_is_not_a_suffix(self):
        self.assertEqual(self.info("github.com/acme/vault", "name"), "vault")
        self.assertEqual(self.info("github.com/acme/vault", "major"), "")

    def test_proxy_path_encodes_uppercase(self):
        self.assertEqual(
            self.info("github.com/Acme/Lib", "proxy-path"), "github.com/!acme/!lib"
        )

    def test_quoted_module_path(self):
        self.assertEqual(self.info('"github.com/acme/lib"', "path"), "github.com/acme/lib")

    def test_repo_go_mod_has_a_module(self):
        r = run(["scripts/module-info.sh", "path"])
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(r.stdout.startswith("github.com/"))


class ModulePathGuard(WithGoMod):
    def check(self, module, version):
        env = {**self.go_mod(module), "RELEASE_VERSION": version}
        return run(["scripts/check-module-path.sh"], env=env)

    def test_v1_without_suffix_passes(self):
        self.assertEqual(self.check("github.com/acme/lib", "1.4.0").returncode, 0)

    def test_v0_prerelease_passes(self):
        self.assertEqual(self.check("github.com/acme/lib", "0.1.0-alpha.1").returncode, 0)

    def test_v2_needs_suffix(self):
        r = self.check("github.com/acme/lib", "2.0.0")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("github.com/acme/lib/v2", r.stderr)

    def test_v2_with_matching_suffix_passes(self):
        self.assertEqual(self.check("github.com/acme/lib/v2", "2.1.0-rc.1").returncode, 0)

    def test_v3_with_v2_suffix_fails(self):
        self.assertNotEqual(self.check("github.com/acme/lib/v2", "3.0.0").returncode, 0)

    def test_v1_with_suffix_fails(self):
        self.assertNotEqual(self.check("github.com/acme/lib/v2", "1.9.0").returncode, 0)

    def test_name_starting_with_v_is_not_a_suffix(self):
        self.assertEqual(self.check("github.com/acme/vault", "1.0.0").returncode, 0)

    def test_bad_version_fails(self):
        self.assertNotEqual(self.check("github.com/acme/lib", "latest").returncode, 0)


class Targets(unittest.TestCase):
    def test_publish_targets_are_noops(self):
        for target in ("publish", "publish-packages", "finalize"):
            # PYPI_PUBLISH & co. are set on purpose: none applies to Go.
            r = run(
                ["make", "-s", target],
                env={"PYPI_PUBLISH": "pypi", "NPM_PUBLISH": "1", "CARGO_PUBLISH": "1"},
            )
            self.assertEqual(r.returncode, 0, r.stderr)

    def test_release_arches(self):
        r = run(["make", "-s", "release-arches"])
        self.assertEqual(r.stdout.strip(), '["amd64"]')

    def test_release_check_needs_a_version(self):
        r = run(["make", "release-check"], env={"RELEASE_VERSION": ""})
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("RELEASE_VERSION is not set", r.stderr)


class Handler(http.server.BaseHTTPRequestHandler):
    # Set per test: a list of (status, body) answers, the last one repeating.
    answers = []
    paths = []

    def do_GET(self):
        type(self).paths.append(self.path)
        answers = type(self).answers
        status, body = answers.pop(0) if len(answers) > 1 else answers[0]
        self.send_response(status)
        self.end_headers()
        self.wfile.write(body.encode())

    def log_message(self, *_):
        pass


class VerifyPublished(WithGoMod):
    def verify(self, answers, tag="v1.2.3", module="github.com/Acme/lib"):
        Handler.answers = list(answers)
        Handler.paths = []
        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        env = {
            **self.go_mod(module),
            "RELEASE_TAG": tag,
            "PROXY_URL": f"http://127.0.0.1:{server.server_port}",
            "VERIFY_ATTEMPTS": "3",
            "VERIFY_SLEEP": "0",
        }
        return run(["scripts/release-verify-published.sh"], env=env)

    def test_found_after_retries(self):
        r = self.verify([(404, "not found"), (410, "gone"), (200, '{"Version":"v1.2.3"}')])
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(Handler.paths[-1], "/github.com/!acme/lib/@v/v1.2.3.info")
        self.assertEqual(len(Handler.paths), 3)

    def test_never_appears_fails(self):
        r = self.verify([(404, "not found")])
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(len(Handler.paths), 3)
        self.assertIn("did not appear", r.stderr)

    def test_wrong_version_fails_at_once(self):
        r = self.verify([(200, '{"Version":"v9.9.9"}')])
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(len(Handler.paths), 1)


if __name__ == "__main__":
    unittest.main()
