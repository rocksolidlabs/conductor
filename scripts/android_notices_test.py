"""Tests for android_notices.py and the Android part of notices.py: Gradle's list read as it writes it, each licence read as its kind,
the committed list held to Gradle's, and the notices naming every package of it."""
from __future__ import annotations

from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import android_notices  # noqa: E402
import notices  # noqa: E402

LISTED = "\n".join([
    "androidx.core:core:1.17.0\tThe Apache Software License, Version 2.0 <http://www.apache.org/licenses/LICENSE-2.0.txt>",
    "com.google.android.gms:play-services-basement:18.5.0\tAndroid Software Development Kit License <https://developer.android.com/studio/terms.html>",
    "org.apache.cordova:framework:14.0.1\tApache License, Version 2.0 <https://www.apache.org/licenses/LICENSE-2.0>",
    "org.bouncycastle:bcprov-jdk18on:1.82\tBouncy Castle Licence <https://www.bouncycastle.org/licence.html>",
    "org.example:odd:1.0\tSome Licence <https://example.org/licence>",
    "org.example:none:2.0\tno licence named",
    "androidx.datastore:datastore-preferences-external-protobuf:1.1.7\tBSD-3-Clause <https://opensource.org/licenses/BSD-3-Clause>",
]) + "\n"


class AndroidNoticesTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)

    def test_gradles_list_is_read_as_it_writes_it(self) -> None:
        packages = android_notices.parse(LISTED)
        self.assertEqual([p.name for p in packages][:2], ["androidx.core:core", "com.google.android.gms:play-services-basement"])
        self.assertEqual(packages[0].version, "1.17.0")
        self.assertEqual(packages[5].licences, [])
        with self.assertRaises(ValueError):
            android_notices.parse("androidx.core:core\tApache\n")

    def test_each_licence_is_read_as_its_kind(self) -> None:
        kinds = [p.kind() for p in android_notices.parse(LISTED)]
        self.assertEqual(kinds[0], ("Apache-2.0", "scripts/notices/apache-2.0.LICENSE"))
        self.assertEqual(kinds[1], ("Android Software Development Kit License", None))
        self.assertEqual(kinds[2][0], "Apache-2.0")
        self.assertEqual(kinds[3], ("MIT (Bouncy Castle Licence)", "scripts/notices/bouncycastle-1.82.LICENSE"))
        self.assertEqual(kinds[4], ("Some Licence <https://example.org/licence>", None))
        self.assertEqual(kinds[5], ("no licence named", None))
        self.assertEqual(kinds[6], ("BSD-3-Clause", "scripts/notices/protobuf.LICENSE"))

    def test_the_committed_list_is_held_to_gradles_and_the_notices_to_the_list(self) -> None:
        fresh, committed, text = self.dir / "fresh.txt", self.dir / "committed.txt", self.dir / "NOTICES"
        said: list[str] = []
        self.assertEqual(android_notices.check(fresh, committed, text, said.append), 1)
        self.assertIn("no ", said[-1])
        fresh.write_text(LISTED)
        said.clear()
        self.assertEqual(android_notices.check(fresh, committed, text, said.append), 1)
        self.assertTrue(any(l.startswith("+androidx.core:core:1.17.0") for l in said), said)
        committed.write_text(LISTED)
        entries = notices.android_entries(committed)
        text.write_text(notices.render([(notices.android_heading(committed), entries)]))
        self.assertEqual(android_notices.check(fresh, committed, text, said.append), 0)
        text.write_text(text.read_text().replace("org.example:odd 1.0", "org.example:other 1.0"))
        said.clear()
        self.assertEqual(android_notices.check(fresh, committed, text, said.append), 1)
        self.assertIn("org.example:odd 1.0", said[-1])

    def test_the_notices_carry_each_packages_licence_or_name_it(self) -> None:
        listed = self.dir / "list.txt"
        listed.write_text(LISTED)
        entries = {e.name: e for e in notices.android_entries(listed)}
        self.assertEqual(entries["androidx.core:core"].licence, "Apache-2.0")
        self.assertIn("Apache License", entries["androidx.core:core"].texts[0][1])
        self.assertIn("developer.android.com/studio/terms", entries["com.google.android.gms:play-services-basement"].texts[0][1])
        self.assertTrue(any("Cordova" in name for name, _ in entries["org.apache.cordova:framework"].texts))
        self.assertIn("Bouncy Castle", entries["org.bouncycastle:bcprov-jdk18on"].texts[0][1])
        listed.write_text(LISTED.replace("bcprov-jdk18on:1.82", "bcprov-jdk18on:1.83"))
        with self.assertRaises(SystemExit):
            notices.android_entries(listed)


if __name__ == "__main__":
    unittest.main()
