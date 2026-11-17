package crew

import (
	"context"
	"encoding/json"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// A candidate's dependency, test and build files (docs/plans/compare-crew.md,
// PR-2): read by name from the diff, and for go.mod and package.json by
// their content at the base and at the commit, with the standard library
// alone. Anything else is listed as a changed file only: the comparison
// never claims a dependency change it did not parse.

// dependencyFiles are the files, at any depth, that name a project's
// dependencies; requirements*.txt is matched as a pattern.
var dependencyFiles = map[string]bool{
	"go.mod": true, "go.sum": true,
	"package.json": true, "package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"pyproject.toml": true, "poetry.lock": true, "Pipfile": true, "Pipfile.lock": true,
	"Cargo.toml": true, "Cargo.lock": true,
	"Gemfile": true, "Gemfile.lock": true,
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"composer.json": true, "composer.lock": true,
}

// isDependencyFile reports whether the path names a dependency file.
func isDependencyFile(p string) bool {
	name := path.Base(p)
	if dependencyFiles[name] {
		return true
	}
	return strings.HasPrefix(name, "requirements") && strings.HasSuffix(name, ".txt")
}

// testSegments are the directories whose files are test files.
var testSegments = map[string]bool{"test": true, "tests": true, "__tests__": true, "testdata": true}

// isTestFile reports whether the path names a test file: by its name
// (*_test.go, test_*.py, *_test.py, *.test.*, *.spec.*, conftest.py) or a
// test, tests, __tests__ or testdata directory on its way.
func isTestFile(p string) bool {
	name := path.Base(p)
	switch {
	case strings.HasSuffix(name, "_test.go"),
		strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py"),
		strings.HasSuffix(name, "_test.py"),
		name == "conftest.py",
		strings.Contains(name, ".test."),
		strings.Contains(name, ".spec."):
		return true
	}
	segs := strings.Split(p, "/")
	return slices.ContainsFunc(segs[:len(segs)-1], func(s string) bool { return testSegments[s] })
}

// buildFiles are the files, at any depth, that can define what a check's
// command runs; package.json is one only when its scripts changed
// (isBuildFile's caller reads them).
var buildFiles = map[string]bool{
	"Makefile": true, "GNUmakefile": true, "justfile": true, "Taskfile.yml": true, "Taskfile.yaml": true,
	"tox.ini": true, "noxfile.py": true, "pytest.ini": true, "setup.cfg": true, "pyproject.toml": true, "go.work": true,
}

// buildPrefixes are the configuration files matched by their name's start
// (jest.config.js, vitest.config.ts, playwright.config.mjs).
var buildPrefixes = []string{"jest.config.", "vitest.config.", "playwright.config."}

// isBuildFile reports whether the path names a build file by its name
// alone (package.json aside).
func isBuildFile(p string) bool {
	name := path.Base(p)
	if buildFiles[name] || strings.HasSuffix(name, ".mk") {
		return true
	}
	return slices.ContainsFunc(buildPrefixes, func(pre string) bool { return strings.HasPrefix(name, pre) })
}

// depsOf reads the candidate's dependency files from its diff: every one
// listed (at most maxDepFiles, with the total), and for a go.mod or a
// package.json the modules added, removed or at another version (at most
// maxDepModules, with their total), each file read at the base and at the
// commit within maxDepBlob, else listed without modules. Nil when it
// changed none.
func depsOf(ctx context.Context, git gitFunc, dir, base, commit string, stats []FileStat) (*DepChanges, error) {
	var dc *DepChanges
	for _, st := range stats {
		if !isDependencyFile(st.Path) {
			continue
		}
		if dc == nil {
			dc = &DepChanges{Files: []string{}, Modules: []ModuleChange{}}
		}
		dc.Total++
		if len(dc.Files) < maxDepFiles {
			dc.Files = append(dc.Files, cutEncoded(st.Path, maxComparePath))
		}
		var parse func(string) (map[string]string, bool)
		switch path.Base(st.Path) {
		case "go.mod":
			parse = func(text string) (map[string]string, bool) { return goModRequires(text), true }
		case "package.json":
			parse = packageDeps
		default:
			continue
		}
		was, wok, err := fileAt(ctx, git, dir, base, st.Path, maxDepBlob)
		if err != nil {
			return dc, err
		}
		now, nok, err := fileAt(ctx, git, dir, commit, st.Path, maxDepBlob)
		if err != nil {
			return dc, err
		}
		if !wok || !nok {
			continue // past its bound: listed without modules
		}
		before, bok := parse(was)
		after, aok := parse(now)
		if !bok || !aok {
			continue // not parsed: listed without modules
		}
		names := slices.Sorted(maps.Keys(before))
		for name := range after {
			if _, ok := before[name]; !ok {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		for _, name := range names {
			from, to := before[name], after[name]
			if from == to {
				continue
			}
			dc.ModulesTotal++
			if len(dc.Modules) < maxDepModules {
				dc.Modules = append(dc.Modules, ModuleChange{File: cutEncoded(st.Path, maxComparePath), Name: cutEncoded(name, maxComparePath),
					From: cutEncoded(from, maxDepVersion), To: cutEncoded(to, maxDepVersion)})
			}
		}
	}
	return dc, nil
}

// fileAt reads the file at path in commit, its size asked first: "" for a
// commit without it, ok false for one past limit.
func fileAt(ctx context.Context, git gitFunc, dir, commit, p string, limit int) (string, bool, error) {
	blob, err := blobAt(ctx, git, dir, commit, p)
	if err != nil || blob == "" {
		return "", true, err
	}
	size, err := git(ctx, dir, 64, "cat-file", "-s", blob)
	if err != nil {
		return "", true, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(size))
	if err != nil {
		return "", true, err
	}
	if n > limit {
		return "", false, nil
	}
	out, err := git(ctx, dir, limit, "cat-file", "blob", blob)
	return out, true, err
}

// goModRequires is a go.mod's require lines, single and in blocks, module
// to version, comments left out.
func goModRequires(text string) map[string]string {
	out := map[string]string{}
	block := false
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case block && f[0] == ")":
			block = false
		case block && len(f) >= 2:
			out[f[0]] = f[1]
		case f[0] == "require" && len(f) == 2 && f[1] == "(":
			block = true
		case f[0] == "require" && len(f) >= 3:
			out[f[1]] = f[2]
		}
	}
	return out
}

// packageMaps are package.json's maps of modules, in the order a name
// found in two is read.
var packageMaps = []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"}

// packageDeps is a package.json's four maps of modules, name to version
// (the first map naming it wins): none for no file (""), ok false for a
// file that is not a JSON object or whose maps are not maps of strings.
func packageDeps(text string) (map[string]string, bool) {
	out := map[string]string{}
	if text == "" {
		return out, true
	}
	var p map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &p) != nil || p == nil {
		return nil, false
	}
	for _, key := range packageMaps {
		raw, ok := p[key]
		if !ok || string(raw) == "null" {
			continue
		}
		var deps map[string]string
		if json.Unmarshal(raw, &deps) != nil {
			return nil, false
		}
		for name, v := range deps {
			if _, ok := out[name]; !ok {
				out[name] = v
			}
		}
	}
	return out, true
}

// packageScripts is a package.json's scripts map; ok false for a file that
// is not JSON or whose scripts are not a map of strings.
func packageScripts(text string) (map[string]string, bool) {
	if text == "" {
		return map[string]string{}, true
	}
	var p struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal([]byte(text), &p) != nil {
		return nil, false
	}
	return p.Scripts, true
}

// verifyFilesOf lists, from the whole diff, each test file and build file
// the candidate changed (at most maxVerifyFiles, with the total): a
// package.json counts when its scripts map differs between the base and
// the commit, or cannot be read at either. Nil when it changed none.
func verifyFilesOf(ctx context.Context, git gitFunc, dir, base, commit string, stats []FileStat) (*FileList, error) {
	var fl *FileList
	add := func(p, kind string) {
		if fl == nil {
			fl = &FileList{Files: []ListedFile{}}
		}
		fl.Total++
		if len(fl.Files) < maxVerifyFiles {
			fl.Files = append(fl.Files, ListedFile{Path: cutEncoded(p, maxComparePath), Kind: kind})
		}
	}
	for _, st := range stats {
		switch {
		case isTestFile(st.Path):
			add(st.Path, FileTest)
		case isBuildFile(st.Path):
			add(st.Path, FileBuild)
		case path.Base(st.Path) == "package.json":
			was, wok, err := fileAt(ctx, git, dir, base, st.Path, maxDepBlob)
			if err != nil {
				return fl, err
			}
			now, nok, err := fileAt(ctx, git, dir, commit, st.Path, maxDepBlob)
			if err != nil {
				return fl, err
			}
			before, bok := packageScripts(was)
			after, aok := packageScripts(now)
			if !wok || !nok || !bok || !aok || !maps.Equal(before, after) {
				add(st.Path, FileBuild)
			}
		}
	}
	return fl, nil
}
