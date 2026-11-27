package agents

import "regexp"

// Antigravity reads hooks from ~/.gemini/config/hooks.json, grouped under
// named top-level keys; Conductor's group is "conductor". It has no launch
// flag for them and no event for waiting on the user (the bell covers that),
// so Install writes the group and nothing is injected. A tool event's entry
// is a matcher with its hooks; a Stop entry is the command hook itself
// (antigravity.google/docs/hooks): a Stop in the matcher form makes agy
// refuse the whole group ("command hook must specify 'command'" in its
// log), which Conductor's file did before round 13 (verified against agy
// 1.2.14 on 2026-10-09).

const (
	agyHooks = ".gemini/config/hooks.json"
	agyKey   = "conductor"
)

var agyAssets = map[string]string{
	"agy-hooks.json": jsonAsset(`{"conductor":{"enabled":true,` +
		hookLists(`{"matcher":"*","hooks":[{"type":"command","command":"{{BIN}} notify --agy-hook"}]}`, "PreToolUse", "PostToolUse") + `,` +
		hookLists(`{"type":"command","command":"{{BIN}} notify --agy-hook"}`, "Stop") + `}}`),
}

func agySteps(hooksDir string) []step {
	return []step{agyHooksStep(hooksDir), skillStep(hooksDir, agySkill)}
}

func agyHooksStep(hooksDir string) step {
	return step{agyHooks, func(h *homeDir) (bool, error) {
		asset, err := assetFor(agyAssets, hooksDir, "agy-hooks.json")
		if err != nil {
			return false, err
		}
		return setJSONKey(h, agyHooks, asset, agyKey)
	}}
}

func agyAdapter() Adapter {
	return Adapter{
		ID:   "agy",
		Name: "Antigravity",
		// Antigravity prints its bare version: "1.2.14" (verified on 2026-10-09).
		Probe:  &Probe{Args: []string{"--version"}, Match: regexp.MustCompile(`(?mi)^\s*(?:antigravity|agy)?\s*v?(\d+\.\d+\.\d+)`), Verified: true},
		Assets: agyAssets,
		Install: func(home, hooksDir string) ([]string, error) {
			return install(home, agySteps(hooksDir)...)
		},
		Status: func(home string) (bool, string) {
			return statusOf(home, agyHooks, agySteps("")...)
		},
		Snippet: func(hooksDir string) string {
			return snippetOf(agyAssets, hooksDir, "agy-hooks.json")
		},
		// The Antigravity CLI reads its global skills from ~/.gemini/antigravity-cli/skills and a workspace's .agents/skills, not ~/.agents/skills (antigravity.google/docs/skills).
		SkillPath: agySkill,
		Events:    []string{"done", "tool_use"},
		Flags:     agyFlags,
	}
}

// agyFlags is how Antigravity's flags are read (`agy --help`, 1.4.0, and
// the recipes tier): Go's flag form under "Usage of agy:", printed on
// standard error, with no value placeholder on any row (so whether a flag
// takes a value is unknown), and a short spelling a row of its own whose
// words name its long one ("Short alias for --continue").
var agyFlags = &FlagsRecipe{
	Args: []string{"--help"},
}
