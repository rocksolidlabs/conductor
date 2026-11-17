#!/usr/bin/env python3
"""The preview fixture's service (web/e2e/preview-capture.spec.ts): a project manifest's service in an attempt's tree.

It serves the fixture's pages on 127.0.0.1 at the port given, each with the bridge the tree names
(CONDUCTOR_PREVIEW_BRIDGE) as a script tag, but for /no-bridge, and with the commit the tree's HEAD was at
when the service served its first page, declared in <meta name="conductor-revision"> and never read again:
a page held at that commit while the tree moves on.
"""
import html
import os
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
BRIDGE = os.environ.get("CONDUCTOR_PREVIEW_BRIDGE", "")
PAGES = {"/": "index.html", "/next": "index.html", "/no-bridge": "index.html", "/motion": "motion.html"}
revision = None


def head():
    try:
        out = subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True, timeout=10)
        return out.stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return ""


class Pages(BaseHTTPRequestHandler):
    def do_GET(self):
        global revision
        path = self.path.split("?", 1)[0].split("#", 1)[0]
        if path == "/ready":
            # The readiness probe's: it reads no revision, so the page's is the tree's at the first page served.
            self.send_response(200)
            self.send_header("Content-Length", "2")
            self.end_headers()
            self.wfile.write(b"ok")
            return
        name = PAGES.get(path)
        if name is None:
            self.send_error(404)
            return
        if revision is None:
            revision = head()
        with open(os.path.join(HERE, name), encoding="utf-8") as f:
            text = f.read()
        bridge = "" if path == "/no-bridge" or not BRIDGE else '<script src="%s"></script>' % html.escape(BRIDGE)
        body = text.replace("<!--BRIDGE-->", bridge).replace("__REVISION__", html.escape(revision)).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Pages).serve_forever()
