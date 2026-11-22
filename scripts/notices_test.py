"""Tests for notices.py's file notices, licence names and printing: python3 -m unittest discover -s scripts -p '*_test.py'"""
from pathlib import Path
import tempfile
import unittest

import notices
from notices import Entry, classify, file_notices, licences, list_line, notice_comments, npm_entry, own_licences, render

GO_LICENCE = "Copyright (c) 2009 The Go Authors. All rights reserved. Redistribution and use in source and binary forms..."


class FileNotices(unittest.TestCase):
    def write(self, name: str, text: str) -> Path:
        p = Path(self.dir.name, name)
        p.write_text(text)
        return p

    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()

    def tearDown(self):
        self.dir.cleanup()

    def test_a_notice_after_the_package_clause(self):
        p = self.write("bitcurve.go", "package bitcurves\n\n// Copyright 2010 The Go Authors. All rights reserved.\n// Copyright 2011 ThePiachu. All rights reserved.\n\n// Package bitelliptic ...\npackage x\n")
        self.assertIn("ThePiachu", "\n".join(notice_comments(p)))
        self.assertEqual([n for n, _ in file_notices([p], Path(self.dir.name), GO_LICENCE)], ["bitcurve.go (the file's own notice)"])

    def test_an_unstarred_block_comment_after_build_lines(self):
        p = self.write("os_bound.go", "//go:build !js\n// +build !js\n\n/*\n   Copyright 2022 The Flux authors.\n\n   Licensed under the Apache License.\n*/\n\npackage osfs\n")
        self.assertEqual(notice_comments(p), ["Copyright 2022 The Flux authors.\n\nLicensed under the Apache License."])

    def test_a_starred_block_and_assembly_comments(self):
        p = self.write("a.go", "/*\n * Copyright 2014 Matthew Endsley\n * All rights reserved\n */\npackage keywrap\n")
        self.assertEqual(notice_comments(p), ["Copyright 2014 Matthew Endsley\nAll rights reserved"])
        s = self.write("memmove_amd64.s", "// Derived from Inferno's libkern/memmove-386.s\n//\n//\tCopyright © 1994-1999 Lucent Technologies Inc.\n\n#include \"textflag.h\"\n")
        self.assertIn("Lucent Technologies", "\n".join(notice_comments(s)))

    def test_a_holder_the_licence_names_adds_nothing_and_one_text_prints_once(self):
        own = self.write("own.go", "// Copyright 2017 The Go Authors. All rights reserved.\n\npackage x\n")
        a = self.write("a.go", "// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>\n// SPDX-License-Identifier: MIT\n\npackage x\n")
        b = self.write("b.go", "// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>\n// SPDX-License-Identifier: MIT\n\npackage x\n")
        got = file_notices([own, a, b], Path(self.dir.name), GO_LICENCE)
        self.assertEqual([n for n, _ in got], ["a.go and 1 more (the file's own notice)"])

    def test_a_later_block_with_another_holder(self):
        p = self.write("log.go", "// Copyright 2009 The Go Authors. All rights reserved.\n\npackage math\n\n/*\n\tThe original C code ...\n*/\n\n// ====================================================\n// Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.\n//\n// Permission to use, copy, modify, and distribute this\n// software is freely granted.\n\nfunc log(x float64) float64 { return x }\n")
        got = file_notices([p], Path(self.dir.name), GO_LICENCE)
        self.assertEqual(len(got), 1)
        self.assertIn("Sun Microsystems", got[0][1])

    def test_a_bundled_module_header(self):
        p = self.write("xterm.mjs", "/**\n * Copyright (c) 2014-2024 The xterm.js authors. All rights reserved.\n * Copyright (c) 2012-2013, Christopher Jeffrey (MIT License)\n * Originally forked from (with the author's permission):\n *   Fabrice Bellard's javascript vt100 for jslinux:\n *   Copyright (c) 2011 Fabrice Bellard\n */\nvar a=1;\n")
        got = file_notices([p], Path(self.dir.name), "Copyright (c) 2017-2019, The xterm.js authors\nCopyright (c) 2012-2013, Christopher Jeffrey (https://github.com/chjj/)\nPermission is hereby granted")
        self.assertEqual(len(got), 1)
        self.assertIn("Fabrice Bellard", got[0][1])

    def test_a_license_banner_with_no_copyright_word(self):
        p = self.write("dompurify.js", "/*! @license DOMPurify 3.4.15 | (c) Cure53 and other contributors | Released under the Apache license 2.0 and Mozilla Public License 2.0 | github.com/cure53/DOMPurify/blob/3.4.15/LICENSE */\n\nfunction f() {}\n")
        got = file_notices([p], Path(self.dir.name), "Copyright (c) 2016 - present Microsoft Corporation\nPermission is hereby granted")
        self.assertEqual(len(got), 1)
        self.assertIn("Cure53", got[0][1])

    def test_code_in_prose_is_no_notice_and_a_named_banner_holder_matches_its_licence(self):
        p = self.write("scan.go", "// Token returns the run of code points c satisfying f(c).\n// Precondition: ishex(c) is true; (C) is not a holder.\npackage fmt\n")
        self.assertEqual(notice_comments(p), [])
        a = self.write("arena.go", "// Copyright 2022 The Go Authors. All rights reserved.\n\npackage runtime\n\n// Chunks:\n//    (a) Reuse one.\n//    (b) Take a fresh one.\n//    (c) Failing all the above, map a new chunk.\nvar x int\n")
        self.assertEqual(file_notices([a], Path(self.dir.name), GO_LICENCE), [])
        v = self.write("shared.js", "/**\n* @vue/shared v3.5.43\n* (c) 2018-present Yuxi (Evan) You and Vue contributors\n* @license MIT\n**/\nexport {}\n")
        self.assertEqual(file_notices([v], Path(self.dir.name), "The MIT License (MIT)\n\nCopyright (c) 2018-present, Yuxi (Evan) You and Vue contributors"), [])

    def test_no_notice(self):
        self.assertEqual(notice_comments(self.write("plain.go", "package x\n\nfunc f() {}\n")), [])

    def test_a_notice_begun_in_the_first_lines_is_read_to_its_end(self):
        lines = [f"// clause {i} of the notice" for i in range(300)]
        p = self.write("errno_darwin.go", "package errno\n\n" + "var x int\n" * 150 + "// Copyright (c) 2000 Apple Computer, Inc. All rights reserved.\n" + "\n".join(lines) + "\n\nvar y int\n")
        got = notice_comments(p)
        self.assertEqual(len(got), 1)
        self.assertEqual(got[0].splitlines()[-1], "clause 299 of the notice")
        self.assertEqual(len(got[0].splitlines()), 301)
        b = self.write("stdio_darwin.go", "package stdio\n\n" + "var x int\n" * 190 + "/*\n * Copyright (c) 1990 The Regents of the University of California.\n" + "".join(f" * term {i}\n" for i in range(100)) + " */\n\nvar y int\n")
        got = notice_comments(b)
        self.assertEqual(len(got), 1)
        self.assertTrue(got[0].endswith("term 99"))

    def test_a_comment_begun_after_the_first_lines_is_not_read(self):
        p = self.write("late.go", "package x\n\n" + "var x int\n" * 300 + "// Copyright 2001 Someone Else.\n")
        self.assertEqual(notice_comments(p), [])


GODS = """Copyright (c) 2015, Emir Pasic
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

* Redistributions of source code must retain the above copyright notice, this
  list of conditions and the following disclaimer.

* Redistributions in binary form must reproduce the above copyright notice,
  this list of conditions and the following disclaimer in the documentation
  and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES ... EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

-------------------------------------------------------------------------------

AVL Tree:

Copyright (c) 2017 Benjamin Scher Purcell <benjapurcell@gmail.com>

Permission to use, copy, modify, and distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS.
"""
BSD3 = ("Copyright (c) 2017 The Sqlite Authors. All rights reserved.\n\nRedistribution and use in source and binary forms, with or without\n"
        "modification, are permitted provided that the following conditions are met:\n\n3. Neither the name of the copyright holder nor the names of its\n"
        "contributors may be used to endorse or promote products derived from this software without specific prior written permission.\n")
BSD2 = "Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met: two of them.\n"
MIT = ("Permission is hereby granted, free of charge, to any person obtaining a copy\nof this software and associated documentation files (the \"Software\"), to deal\n"
       "in the Software without restriction. The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.\n")
APACHE = "                                 Apache License\n                           Version 2.0, January 2004\n\n   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION\n"
OFL = ("This Font Software is licensed under the SIL Open Font License, Version 1.1.\n\nPermission is hereby granted, free of charge, to any person obtaining\n"
       "a copy of the Font Software, to use, study, copy, merge, embed, modify, redistribute, and sell modified and unmodified copies of the Font Software.\n")


class Classify(unittest.TestCase):
    def test_each_licence_by_its_own_text(self):
        cases = {
            APACHE: "Apache-2.0",
            "Mozilla Public License Version 2.0\n==================================\n1. Definitions": "MPL-2.0",
            OFL: "OFL-1.1",
            MIT: "MIT",
            "University of Illinois/NCSA Open Source License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy of this software, to deal with the Software without restriction: Redistributions of source code must retain the above copyright notice.": "",
            "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted, provided that the above copyright notice and this permission notice appear in all copies.": "ISC",
            "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted.\n\nTHE SOFTWARE IS PROVIDED \"AS IS\"": "0BSD",
            BSD2: "BSD-2-Clause",
            BSD3: "BSD-3-Clause",
            BSD2 + "3. All advertising materials mentioning features or use of this software must display an acknowledgement.": "BSD-4-Clause",
            "This is free and unencumbered software released into the public domain.": "Unlicense",
            "Licensed under terms this script does not know.": "",
        }
        for text, want in cases.items():
            with self.subTest(want=want):
                self.assertEqual(classify(text), want)

    def test_a_text_carrying_two_licences_names_both(self):
        self.assertEqual(classify(GODS), "BSD-2-Clause, ISC")
        self.assertEqual(licences(MIT + "\n\nPortions:\n\n" + APACHE), ["Apache-2.0", "MIT"])

    def test_each_grant_is_read_from_its_own_terms(self):
        # The third clause of the later BSD text is its own: the earlier one stays two-clause.
        self.assertEqual(licences(BSD2 + "\n\nThe parser:\n\n" + BSD3), ["BSD-2-Clause", "BSD-3-Clause"])

    def test_one_licence_holding_words_of_another_is_that_licence_alone(self):
        self.assertEqual(licences(OFL), ["OFL-1.1"])

    def test_a_list_of_others_licences_does_not_name_the_package(self):
        texts = [("LICENSE", BSD3), ("LICENSE-3RD-PARTY.md", "# Third-Party Licenses\n\n" + APACHE + MIT), ("NOTICE", MIT)]
        self.assertEqual(own_licences(texts), ["BSD-3-Clause"])
        self.assertEqual(own_licences([("LICENSE", GODS), ("LICENSE-ADDITIONAL", MIT)]), ["BSD-2-Clause", "ISC", "MIT"])


class ListLine(unittest.TestCase):
    def package(self, files: dict[str, str]) -> Path:
        d = Path(self.dir.name, "pkg")
        d.mkdir()
        (d / "package.json").write_text('{"name": "example-pkg", "version": "1.0.0"}')
        for name, text in files.items():
            (d / name).write_text(text)
        return d

    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()

    def tearDown(self):
        self.dir.cleanup()

    def test_a_package_carrying_others_notices_says_so_beside_its_own_licence(self):
        e = npm_entry(self.package({"LICENSE": BSD3, "LICENSE-3RD-PARTY.md": "# Third-Party Licenses\n\n" + APACHE + MIT}))
        self.assertEqual(e.licence, "BSD-3-Clause; carries notices of other projects' code, printed below")
        out = render([("npm packages", [e])])
        self.assertIn(f"  {'example-pkg 1.0.0':<56} BSD-3-Clause; carries notices of other projects' code, printed below\n", out)

    def test_a_package_with_its_licence_alone_gets_no_words(self):
        self.assertEqual(npm_entry(self.package({"LICENSE": GODS})).licence, "BSD-2-Clause, ISC")
        self.assertEqual(list_line("MIT", [("LICENSE", MIT)]), "MIT")
        self.assertEqual(list_line("Apache-2.0", [("LICENSE", APACHE), ("NOTICE", "Copyright 2014 Someone")]),
                         "Apache-2.0; carries notices of other projects' code, printed below")


class Render(unittest.TestCase):
    def test_a_long_licence_is_printed_whole(self):
        text = "\n".join(f"term {i}" for i in range(1000))
        out = render([("Go modules", [Entry("example.com/long", "v1.0.0", "MIT", [("LICENSE", text)])])])
        self.assertIn(text, out)

    def test_a_text_past_the_limit_stops_with_its_package_named(self):
        text = "\n".join("x" for _ in range(notices.MAX_TEXT_LINES + 1))
        ok = "\n".join("x" for _ in range(notices.MAX_TEXT_LINES))
        render([("Go modules", [Entry("example.com/fits", "v1.0.0", "MIT", [("LICENSE", ok)])])])
        with self.assertRaises(SystemExit) as stop:
            render([("Go modules", [Entry("example.com/fits", "v1.0.0", "MIT", [("LICENSE", "MIT")]),
                                    Entry("example.com/huge", "v2.0.0", "MIT", [("LICENSE", "MIT"), ("COPYING", text)])])])
        self.assertIn("example.com/huge v2.0.0: COPYING", str(stop.exception))
        self.assertNotIn("example.com/fits", str(stop.exception))


if __name__ == "__main__":
    unittest.main()
