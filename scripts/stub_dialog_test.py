# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 the Conductor Authors and RockSolid Labs, Inc.
"""The e2e stub's dialog keys (web/e2e/stub-agent.sh, dialog_key, #716).

The stub's probe dialog (STUB_DIALOG_PROBE) is driven through a pty with its
read's timer made short (STUB_DIALOG_WAIT): every key is read, an Enter as
much as any other byte, each sent as the read's timer ends; none is echoed;
a resize sent as the dialog is first drawn is drawn; a resize that comes
with the dialog's last key is drawn before the dialog ends.
"""

import fcntl
import os
from pathlib import Path
import pty
import random
import re
import select
import shutil
import signal
import struct
import subprocess
import tempfile
import termios
import time
import unittest

STUB = Path(__file__).resolve().parent.parent / "web" / "e2e" / "stub-agent.sh"
# The read's timer, in seconds, and the keys sent one timer apart: Enters
# and letters, several hundred of each.
WAIT = 0.005
EACH = 400
# What bounds a hang: no step of the probe takes a tenth of it.
HANG = 20.0
ESCAPES = re.compile(rb"\x1b\[[0-9;?]*[A-Za-z]")


def bash():
    """The bash the stub runs with (4 or later, as the e2e suite asks), or None."""
    path = shutil.which("bash")
    if path is None:
        return None
    out = subprocess.run([path, "-c", "echo ${BASH_VERSINFO[0]}"], capture_output=True, text=True, check=False).stdout.strip()
    return path if out.isdigit() and int(out) >= 4 else None


class Probe:
    """The stub's probe dialog in a pty, and what it has drawn."""

    def __init__(self, shell, home, keys):
        env = {k: v for k, v in os.environ.items() if not k.startswith("STUB_")}
        env.update({"HOME": home, "STUB_DIALOG_PROBE": keys, "STUB_DIALOG_WAIT": str(WAIT), "TERM": "xterm"})
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            try:
                os.execve(shell, [shell, str(STUB)], env)
            finally:
                os._exit(127)
        self.out = b""

    def pump(self, seconds):
        """Reads what the stub draws for that long."""
        end = time.monotonic() + seconds
        while (left := end - time.monotonic()) > 0:
            ready, _, _ = select.select([self.fd], [], [], left)
            if not ready:
                return
            try:
                chunk = os.read(self.fd, 65536)
            except OSError:
                return
            if not chunk:
                return
            self.out += chunk

    def until(self, text):
        """Reads until the screen's text holds text, or fails after HANG."""
        end = time.monotonic() + HANG
        while text not in self.text():
            if time.monotonic() > end:
                raise AssertionError(f"the stub never drew {text!r}: {self.out!r}")
            self.pump(0.05)

    def text(self):
        return ESCAPES.sub(b"", self.out).decode("utf-8", "replace")

    def resize(self, rows, cols):
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def close(self):
        try:
            os.kill(self.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        os.waitpid(self.pid, 0)
        os.close(self.fd)


@unittest.skipUnless(hasattr(pty, "fork") and bash(), "needs a pty and bash 4 or later")
class StubDialogKeysTest(unittest.TestCase):
    def test_every_key_is_read_none_is_echoed_and_every_resize_is_drawn(self):
        with tempfile.TemporaryDirectory() as home:
            keys = os.path.join(home, "keys")
            probe = Probe(bash(), home, keys)
            try:
                probe.until("probe draw 1")
                # A resize and keys as soon as the dialog shows.
                probe.resize(30, 100)
                first = [b"x"] * 5
                os.write(probe.fd, b"".join(first))
                probe.until("probe draw 2")
                # Enters and letters, each as the read's timer ends.
                sent = list(first)
                for i in range(2 * EACH):
                    key = b"\r" if i % 2 == 0 else b"y"
                    os.write(probe.fd, key)
                    sent.append(key)
                    probe.pump(random.uniform(0.9 * WAIT, 1.1 * WAIT))
                # A resize with the key that ends the dialog, both during one
                # read: w makes the reads after it wait 30 s at a time.
                os.write(probe.fd, b"w")
                sent.append(b"w")
                end = time.monotonic() + HANG
                while not read_keys(keys)[-1:] == ["w"]:
                    if time.monotonic() > end:
                        raise AssertionError("the stub never read w")
                    probe.pump(0.01)
                probe.resize(24, 80)
                os.write(probe.fd, b"q")
                sent.append(b"q")
                probe.until("probe end")
            finally:
                probe.close()
            read = read_keys(keys)
            want = ["$'\\n'" if k == b"\r" else k.decode() for k in sent]
            self.assertEqual(len(read), len(want), "keys read")
            self.assertEqual(read, want)
            # Nothing but the dialog's draws, each with the terminal echoing
            # nothing and handing each byte over, and the resize trapped
            # from the first draw: no key echoed. The resize with the last
            # key is drawn before the dialog ends (as it ends, once nothing
            # more is trapped, or at the read's next turn).
            mode = "echo off, icanon off, resize"
            self.assertRegex(probe.text(), f"^probe draw 1: {mode} trapped\r\nprobe draw 2: {mode} trapped\r\nprobe draw 3: {mode} (trapped|no)\r\nprobe end\r\n$")


def read_keys(path):
    """The keys the probe has written, one a row; none before its file."""
    try:
        with open(path, encoding="utf-8") as f:
            return f.read().splitlines()
    except FileNotFoundError:
        return []


if __name__ == "__main__":
    unittest.main()
