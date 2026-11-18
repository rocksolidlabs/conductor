#!/usr/bin/env python3
"""The retake spec's service (web/e2e/capture-retake.spec.ts): a project manifest's service in a tree.

It serves the tree's own app/page.html (read from the working directory at each request, so a checkout
serves what its commit holds) on 127.0.0.1 at the port given, with the bridge the tree names
(CONDUCTOR_PREVIEW_BRIDGE) as a script tag, the commit the tree's HEAD was at when the service served
its first page declared in <meta name="conductor-revision">, and the folder it runs in written for
__TREE_JSON__, so the page's component files are absolute paths in the tree, as a Vue dev build's are.
"""
import html
import json
import os
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BRIDGE = os.environ.get("CONDUCTOR_PREVIEW_BRIDGE", "")
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
            body = b"ok"
        elif path == "/":
            if revision is None:
                revision = head()
            with open(os.path.join("app", "page.html"), encoding="utf-8") as f:
                text = f.read()
            bridge = '<script src="%s"></script>' % html.escape(BRIDGE) if BRIDGE else ""
            body = text.replace("<!--BRIDGE-->", bridge).replace("__REVISION__", html.escape(revision)).replace("__TREE_JSON__", json.dumps(os.getcwd())).encode("utf-8")
        else:
            self.send_error(404)
            return
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
