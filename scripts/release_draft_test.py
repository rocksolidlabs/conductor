"""Tests for release_draft.py: python3 -m unittest discover -s scripts -p '*_test.py'"""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import release_draft
from release_draft import built, draft, plan, problems, valid

SHA = "8ca09b3c108e1e7ef958d546bcb429021bf7c2c9"
OTHER = "eec045aa87dcb8aabc760341cfd05ea6a25b9542"


def release(rid, tag, draft=True, assets=(), target=SHA):
    return {"id": rid, "tag_name": tag, "target_commitish": target, "draft": draft,
            "assets": [{"name": a, "state": "uploaded"} for a in assets]}


class Valid(unittest.TestCase):
    def test_a_version_and_a_commit_pass(self):
        for tag in ("v0.7.0-rc.18", "v1.0.0", "v0.7.0", "v1.2.3-alpha.0", "v1.2.3-x-y.1a", "v10.20.30-rc.10"):
            valid(tag, SHA)

    def test_what_semver_git_or_a_shell_would_read_otherwise_is_refused(self):
        for tag in ("0.7.0-rc.18", "v01.2.3", "v1.02.3", "v1.2.3-01", "v1.2.3-rc..1", "v1.2.3-rc.", "v1.2.3-",
                    "v1.2", "v1.2.3+build.1", "v1.2.3\n", "v1.2.3 ", " v1.2.3", "v1.2.3-rc.1;rm", "v1.2.3-rc/1",
                    "v1.2.3-rc.1?x=1", "v1.2.3-$(id)", "v", ""):
            with self.assertRaises(ValueError, msg=repr(tag)):
                valid(tag, SHA)

    def test_a_commit_is_its_full_id(self):
        for target in ("8ca09b3", "main", "refs/heads/main", SHA.upper(), SHA + "\n", ""):
            with self.assertRaises(ValueError, msg=repr(target)):
                valid("v0.7.0-rc.18", target)


class Ensure(unittest.TestCase):
    def test_a_tag_with_no_release_gets_one_made(self):
        self.assertEqual(plan([release(1, "v0.7.0-rc.16", draft=False)], "v0.7.0-rc.17", SHA), ("create", None))

    def test_a_rerun_keeps_the_draft_the_first_run_made(self):
        self.assertEqual(plan([release(7, "v0.7.0-rc.17")], "v0.7.0-rc.17", SHA), ("keep", 7))

    def test_a_draft_for_another_commit_is_refused(self):
        # A second dispatch of the version from another commit would put two builds on one draft.
        with self.assertRaisesRegex(ValueError, f"draft release 7 is for {OTHER}, not {SHA}"):
            plan([release(7, "v0.7.0-rc.17", target=OTHER)], "v0.7.0-rc.17", SHA)

    def test_a_draft_tagged_with_the_bare_version_is_refused_not_kept(self):
        # electron-builder took 0.7.0-rc.17 for v0.7.0-rc.17; that tag is not the one checked to be absent.
        with self.assertRaisesRegex(ValueError, "draft release 7 is tagged 0.7.0-rc.17"):
            plan([release(7, "0.7.0-rc.17")], "v0.7.0-rc.17", SHA)

    def test_a_published_release_is_not_built_into(self):
        for tag in ("v0.7.0-rc.17", "0.7.0-rc.17"):
            with self.assertRaisesRegex(ValueError, "published release \\(3\\)"):
                plan([release(3, tag, draft=False)], "v0.7.0-rc.17", SHA)

    def test_two_drafts_are_refused_with_their_ids(self):
        with self.assertRaisesRegex(ValueError, "2 draft releases \\(4, 5\\)"):
            plan([release(4, "v0.7.0-rc.17"), release(5, "0.7.0-rc.17")], "v0.7.0-rc.17", SHA)


class Upload(unittest.TestCase):
    def test_upload_takes_the_tags_one_draft(self):
        self.assertEqual(draft([release(1, "v0.7.0-rc.16", draft=False), release(7, "v0.7.0-rc.17")], "v0.7.0-rc.17", SHA)["id"], 7)

    def test_upload_never_makes_a_release(self):
        with self.assertRaisesRegex(ValueError, "no draft release"):
            draft([], "v0.7.0-rc.17", SHA)

    def test_upload_refuses_two_drafts_a_published_release_and_another_commits_draft(self):
        # A rerun of an old build's failed job, after the version's draft was made again for a newer commit.
        for rs in ([release(4, "v0.7.0-rc.17"), release(5, "v0.7.0-rc.17")], [release(3, "v0.7.0-rc.17", draft=False)],
                   [release(6, "v0.7.0-rc.17", target=OTHER)]):
            with self.assertRaises(ValueError):
                draft(rs, "v0.7.0-rc.17", SHA)


class Check(unittest.TestCase):
    # rc.17's names on its release, the Windows installer as electron-builder published it.
    PACKAGES = ["Conductor-0.7.0-rc.17.dmg", "Conductor-0.7.0-rc.17.dmg.blockmap", "latest-mac.yml",
                "Conductor-Setup-0.7.0-rc.17.exe", "Conductor-Setup-0.7.0-rc.17.exe.blockmap", "latest.yml",
                "Conductor-0.7.0-rc.17.AppImage", "conductor-desktop_0.7.0-rc.17_amd64.deb", "latest-linux.yml"]

    def test_one_draft_with_every_package_passes(self):
        r = release(7, "v0.7.0-rc.17", assets=self.PACKAGES)
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, self.PACKAGES, False), [])

    def test_an_installer_without_its_blockmap_fails(self):
        on = [p for p in self.PACKAGES if p != "Conductor-Setup-0.7.0-rc.17.exe.blockmap"]
        r = release(7, "v0.7.0-rc.17", assets=on)
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, on, False), ["Conductor-Setup-0.7.0-rc.17.exe has no Conductor-Setup-0.7.0-rc.17.exe.blockmap on v0.7.0-rc.17's release 7"])

    def test_packages_split_over_two_drafts_fail(self):
        a = release(7, "v0.7.0-rc.17", assets=self.PACKAGES[:5])
        b = release(8, "v0.7.0-rc.17", assets=self.PACKAGES[5:])
        self.assertEqual(problems([a, b], "v0.7.0-rc.17", SHA, self.PACKAGES, False), ["v0.7.0-rc.17 has 2 releases (7, 8), not one draft"])

    def test_a_package_missing_from_the_draft_is_named(self):
        r = release(7, "v0.7.0-rc.17", assets=[p for p in self.PACKAGES if p != "latest.yml"])
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, self.PACKAGES, False), ["latest.yml is not on v0.7.0-rc.17's release 7"])

    def test_an_upload_not_finished_is_not_on_the_release(self):
        r = release(7, "v0.7.0-rc.17", assets=self.PACKAGES)
        r["assets"][5]["state"] = "starter"
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, self.PACKAGES, False), ["latest.yml is not on v0.7.0-rc.17's release 7"])

    def test_no_release_fails(self):
        self.assertEqual(problems([], "v0.7.0-rc.17", SHA, self.PACKAGES, False), ["v0.7.0-rc.17 has 0 releases (none), not one draft"])

    def test_a_published_release_fails(self):
        r = release(7, "v0.7.0-rc.17", draft=False, assets=self.PACKAGES)
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, self.PACKAGES, False), ["v0.7.0-rc.17's release 7 is not a draft"])

    def test_a_tag_that_exists_fails(self):
        r = release(7, "v0.7.0-rc.17", assets=self.PACKAGES)
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, self.PACKAGES, True),
                         ["v0.7.0-rc.17 exists before its release is published: the updater lists it (#375)"])

    def test_a_draft_for_another_commit_fails(self):
        r = release(7, "v0.7.0-rc.17", assets=self.PACKAGES, target=OTHER)
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, self.PACKAGES, False), [f"v0.7.0-rc.17's release 7 is for {OTHER}, not {SHA}"])

    def test_a_draft_under_the_bare_version_fails(self):
        r = release(7, "0.7.0-rc.17", assets=self.PACKAGES)
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, self.PACKAGES, False), ["v0.7.0-rc.17's release 7 is tagged 0.7.0-rc.17"])

    def test_the_updaters_files_are_required_even_when_no_build_made_them(self):
        # A package job that wrote no latest-mac.yml has nothing of it to miss on the release; the macOS apps would.
        built_ = [p for p in self.PACKAGES if p != "latest-mac.yml"]
        r = release(7, "v0.7.0-rc.17", assets=built_)
        self.assertEqual(problems([r], "v0.7.0-rc.17", SHA, built_, False), ["latest-mac.yml is not on v0.7.0-rc.17's release 7"])

    def test_the_packages_are_a_builds_published_files_at_its_top(self):
        with tempfile.TemporaryDirectory() as d:
            for name in ["Conductor-Setup-0.7.0-rc.17.exe", "Conductor-Setup-0.7.0-rc.17.exe.blockmap", "latest.yml",
                         "builder-debug.yml", "builder-effective-config.yaml", "conductor-desktop-0.7.0-rc.17-x64.nsis.7z",
                         "win-unpacked/Conductor.exe", "mac/Conductor.app/Contents/Info.plist"]:
                p = Path(d, name)
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text("x")
            self.assertEqual(built(Path(d)), ["Conductor-Setup-0.7.0-rc.17.exe", "Conductor-Setup-0.7.0-rc.17.exe.blockmap", "latest.yml"])


class Gh(unittest.TestCase):
    def test_ghs_output_is_read_as_utf8_whatever_the_locale(self):
        line = '{"id": 7, "tag_name": "v0.7.0-rc.17", "draft": true, "assets": [], "note": "\u201cquoted\u201d"}\n'
        def run(argv, **kw):
            # As subprocess decodes: the bytes gh wrote, with the encoding asked for (the locale's if none).
            return subprocess.CompletedProcess(argv, 0, stdout=line.encode("utf-8").decode(kw.get("encoding") or "cp1252"))
        with mock.patch.object(release_draft.subprocess, "run", side_effect=run):
            self.assertEqual(release_draft.releases("o/r")[0]["note"], "\u201cquoted\u201d")

    def test_releases_are_read_without_their_notes(self):
        with mock.patch.object(release_draft, "gh", return_value="") as gh:
            release_draft.releases("o/r")
        self.assertNotIn("body", gh.call_args.args[-1])
        self.assertIn("{id, tag_name, target_commitish, draft, assets:", gh.call_args.args[-1])

    def test_a_tag_exists_only_when_its_exact_ref_is_listed(self):
        # matching-refs matches a prefix: v1.0.0 lists v1.0.0-rc.1.
        for out, want in (("", False), ("refs/tags/v1.0.0-rc.1\nrefs/tags/v1.0.0.1\n", False), ("refs/tags/v1.0.0-rc.1\nrefs/tags/v1.0.0\n", True)):
            with mock.patch.object(release_draft, "gh", return_value=out) as gh:
                self.assertEqual(release_draft.tag_exists("o/r", "v1.0.0"), want, out)
            self.assertEqual(gh.call_args.args[1], "repos/o/r/git/matching-refs/tags/v1.0.0")


class Main(unittest.TestCase):
    """The commands against a fake gh: what each calls and refuses."""

    def run_main(self, argv, refs="", rels=(), folder=None):
        calls = []
        def gh(*args):
            calls.append(args)
            url = next(a for a in args if a.startswith(("repos/", "https://")))
            if "/git/matching-refs/" in url:
                return refs
            if url.endswith("/releases?per_page=100"):
                return "".join(json.dumps(r) + "\n" for r in rels)
            if url.endswith("/assets?per_page=100"):
                return ""
            return json.dumps({"id": 99})
        with mock.patch.object(release_draft, "gh", side_effect=gh), mock.patch.dict(release_draft.os.environ, {"GITHUB_REPOSITORY": "o/r"}):
            code = release_draft.main(argv)
        return code, calls

    def test_ensure_makes_the_draft_at_the_commit_under_a_tag_that_does_not_exist(self):
        code, calls = self.run_main(["ensure", "v0.7.0-rc.18", "--target", SHA])
        self.assertEqual(code, 0)
        made = [c for c in calls if c[1] == "repos/o/r/releases"]
        self.assertEqual(len(made), 1)
        self.assertIn(f"target_commitish={SHA}", made[0])
        self.assertIn("tag_name=v0.7.0-rc.18", made[0])
        self.assertIn("draft=true", made[0])

    def test_the_draft_ensure_makes_carries_the_checklist_with_the_design_capture(self):
        _, calls = self.run_main(["ensure", "v0.7.0-rc.18", "--target", SHA])
        made = next(c for c in calls if c[1] == "repos/o/r/releases")
        body = [a for a in made if a.startswith("body=")]
        self.assertEqual(body, [f"body={release_draft.CHECKLIST}"])
        self.assertIn("Run `make design-capture` at the release commit, read what drifted, and commit `docs/design/system.json` with `--write`", body[0])

    def test_a_draft_kept_for_the_commit_is_not_rewritten(self):
        code, calls = self.run_main(["ensure", "v0.7.0-rc.18", "--target", SHA], rels=[release(7, "v0.7.0-rc.18")])
        self.assertEqual(code, 0)
        # Read, never made again nor edited: no call makes a release or names the draft kept.
        self.assertEqual([c for c in calls if "repos/o/r/releases" in c or any("releases/7" in a for a in c)], [])
        self.assertFalse(any(a.startswith("body=") for c in calls for a in c), calls)

    def test_ensure_and_upload_refuse_a_tag_that_exists_before_anything_else(self):
        with tempfile.TemporaryDirectory() as d:
            Path(d, "latest.yml").write_text("x")
            for argv in (["ensure", "v0.7.0-rc.18", "--target", SHA], ["upload", "v0.7.0-rc.18", d, "--target", SHA]):
                code, calls = self.run_main(argv, refs="refs/tags/v0.7.0-rc.18\n", rels=[release(7, "v0.7.0-rc.18")])
                self.assertEqual(code, 1, argv)
                self.assertEqual([c[1] for c in calls], ["repos/o/r/git/matching-refs/tags/v0.7.0-rc.18"], argv)

    def test_a_bad_version_or_commit_calls_nothing(self):
        for argv in (["ensure", "v0.7.0-rc..18", "--target", SHA], ["ensure", "v0.7.0-rc.18", "--target", "main"]):
            code, calls = self.run_main(argv)
            self.assertEqual((code, calls), (1, []), argv)

    def test_upload_puts_the_packages_on_the_draft_for_the_commit(self):
        with tempfile.TemporaryDirectory() as d:
            Path(d, "latest.yml").write_text("x")
            code, calls = self.run_main(["upload", "v0.7.0-rc.18", d, "--target", SHA], rels=[release(7, "v0.7.0-rc.18")])
            self.assertEqual(code, 0)
            self.assertTrue(any(c[2:3] == ("POST",) and "/releases/7/assets?name=latest.yml" in c[3] for c in calls), calls)
            code, calls = self.run_main(["upload", "v0.7.0-rc.18", d, "--target", SHA], rels=[release(7, "v0.7.0-rc.18", target=OTHER)])
            self.assertEqual(code, 1)
            self.assertFalse(any("POST" in c for c in calls), calls)

    def test_check_reports_a_tag_pushed_meanwhile(self):
        with tempfile.TemporaryDirectory() as d:
            for name in Check.PACKAGES:
                Path(d, name).write_text("x")
            rels = [release(7, "v0.7.0-rc.18", assets=[p.replace("rc.17", "rc.18") for p in Check.PACKAGES])]
            for name in Check.PACKAGES:
                Path(d, name).rename(Path(d, name.replace("rc.17", "rc.18")))
            self.assertEqual(self.run_main(["check", "v0.7.0-rc.18", d, "--target", SHA], rels=rels)[0], 0)
            self.assertEqual(self.run_main(["check", "v0.7.0-rc.18", d, "--target", SHA], refs="refs/tags/v0.7.0-rc.18\n", rels=rels)[0], 1)


if __name__ == "__main__":
    unittest.main()
