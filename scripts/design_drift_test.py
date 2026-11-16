"""Tests for design_drift.py: each kind of drift on its own line, nothing drifted, --check and a stable --write."""
from __future__ import annotations

import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import design_drift  # noqa: E402

SYSTEM_MD = """# Conductor's design system

## The rules

### Status

Decided 2026-11-16 by the maintainers, #918.

Five states.

### Toolbar

Described, not decided.

One row.

## Elsewhere

### Not a rule
"""

LOOK = {
    "pages": ["home", "yard"],
    "tokens": {"dark": {"--ui-bg": "#0c1410", "--ui-text-muted": "#a3ada4"}, "light": {"--ui-bg": "#ffffff", "--ui-text-muted": "#666e67"}},
    "status": {"finished": {"words": ["finished"], "colour": {"dark": "#8a948b", "light": "#6b756c"}}, "needs": {"words": ["needs you"], "colour": {"dark": "#f0a020", "light": "#d08000"}}},
    "contrast": [{"page": "home", "theme": "dark", "width": 1440, "text": "x", "ratio": 4.6, "floor": 4.5}],
}


class Drift(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "web/app/components").mkdir(parents=True)
        (self.root / "web/test-results/look").mkdir(parents=True)
        (self.root / "docs/design").mkdir(parents=True)
        for name in ("StatusMark", "PageEmpty"):
            (self.root / f"web/app/components/{name}.vue").write_text("<template><i /></template>\n")
        (self.root / "docs/design/system.md").write_text(SYSTEM_MD)
        self.look(LOOK)

    def tearDown(self):
        self.tmp.cleanup()

    def look(self, data):
        (self.root / "web/test-results/look/look.json").write_text(json.dumps(data))

    def run_main(self, *args):
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            code = design_drift.main(["--root", str(self.root), *args])
        return code, out.getvalue()

    def baseline(self):
        return (self.root / "docs/design/system.json").read_text()

    def test_equal_inputs_print_nothing_drifted_and_exit_0(self):
        self.assertEqual(self.run_main("--write")[0], 0)
        self.assertEqual(self.run_main(), (0, "Nothing drifted.\n"))
        self.assertEqual(self.run_main("--check"), (0, "Nothing drifted.\n"))

    def test_each_kind_of_drift_is_reported_on_its_own_line(self):
        self.run_main("--write")
        look = json.loads(json.dumps(LOOK))
        look["tokens"]["light"]["--ui-text-muted"] = "#5f6760"
        look["status"]["finished"]["words"] = ["done"]
        look["status"]["needs"]["colour"]["dark"] = "#e09010"
        look["pages"].append("history")
        self.look(look)
        (self.root / "web/app/components/IconButton.vue").write_text("<template><i /></template>\n")
        (self.root / "web/app/components/PageEmpty.vue").unlink()
        md = self.root / "docs/design/system.md"
        md.write_text(SYSTEM_MD.replace("Described, not decided.", "Decided 2026-11-16 by the maintainers, #927."))
        code, out = self.run_main()
        self.assertEqual(code, 0)
        self.assertEqual(
            out.splitlines(),
            [
                "token `--ui-text-muted` (light): #666e67 to #5f6760",
                "status word (finished): finished to done",
                "status colour (needs, dark): #f0a020 to #e09010",
                "component added: `IconButton`",
                "component gone: `PageEmpty`",
                "page added: `history`",
                "rule Toolbar: Described to Decided",
            ],
        )

    def test_a_rule_added_gone_or_redated_and_a_token_added_are_named(self):
        self.run_main("--write")
        md = self.root / "docs/design/system.md"
        md.write_text(SYSTEM_MD.replace("#918", "#952").replace("### Toolbar", "### The toolbar"))
        look = json.loads(json.dumps(LOOK))
        look["tokens"]["dark"]["--ui-bg-canvas"] = "#0c1410"
        self.look(look)
        _, out = self.run_main()
        self.assertEqual(
            out.splitlines(),
            [
                "token added `--ui-bg-canvas` (dark): #0c1410",
                "rule Status: Decided 2026-11-16, #918 to Decided 2026-11-16, #952",
                "rule added: The toolbar (Described)",
                "rule gone: Toolbar",
            ],
        )

    def test_check_exits_1_on_any_drift(self):
        self.run_main("--write")
        (self.root / "web/app/components/IconButton.vue").write_text("<template><i /></template>\n")
        self.assertEqual(self.run_main("--check"), (1, "component added: `IconButton`\n"))

    def test_write_is_stable_and_sorted(self):
        self.run_main("--write")
        first = self.baseline()
        self.run_main("--write")
        self.assertEqual(self.baseline(), first)
        data = json.loads(first)
        self.assertEqual(list(data), sorted(data))
        self.assertEqual(data["components"], ["PageEmpty", "StatusMark"])
        self.assertEqual(data["rules"], {"Status": "Decided 2026-11-16, #918", "Toolbar": "Described"})
        self.assertNotIn("Not a rule", data["rules"])

    def test_no_capture_is_refused_and_no_baseline_is_said(self):
        (self.root / "web/test-results/look/look.json").unlink()
        self.assertEqual(self.run_main()[0], 2)
        self.look(LOOK)
        self.assertEqual(self.run_main("--check")[0], 1)
        self.assertEqual(self.run_main()[0], 0)

    def test_the_committed_baseline_is_as_write_leaves_it(self):
        # Written by --write, never by hand: sorted keys, one form. What it says is a release's report, never a check here.
        text = (Path(__file__).resolve().parent.parent / "docs/design/system.json").read_text(encoding="utf-8")
        self.assertEqual(design_drift.dump(json.loads(text)), text)

if __name__ == "__main__":
    unittest.main()
