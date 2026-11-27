package agents

import "regexp"

// Goose loads plugins from ~/.agents/plugins/<name>/, hooks from the plugin's
// hooks/hooks.json. Conductor's plugin is a directory of its own there, which
// Install copies whole, with the Conductor skill in ~/.agents/skills. Goose
// fires no event while it waits for the user: the bell or a screen pattern
// covers that.

const gooseDir = ".agents/plugins/conductor/"

var gooseAssets = map[string]string{
	"goose/hooks/hooks.json": jsonAsset(`{"hooks":{` + hookLists(`{"hooks":[{"type":"command","command":"{{BIN}} notify --goose-hook"}]}`,
		"Stop", "UserPromptSubmit", "PreToolUse", "PostToolUse") + `}}`),
}

func gooseSteps(hooksDir string) []step {
	return append(copyAssetDir(gooseAssets, hooksDir, "goose/", gooseDir), skillStep(hooksDir, agentsSkill))
}

func gooseAdapter() Adapter {
	return Adapter{
		ID:   "goose",
		Name: "Goose",
		// Block's goose 1.54.0 prints its bare version (" 1.54.0", verified
		// on 2026-10-09); older releases printed "goose <version>". The Go
		// migrations tool of the same name prints "goose version: v3.x", seen
		// on a PATH where it stood in for the agent: a known impostor.
		Probe:  &Probe{Args: []string{"--version"}, Match: regexp.MustCompile(`(?m)^\s*(?:goose\s+)?v?(\d+\.\d+\.\d+)\s*$`), Reject: regexp.MustCompile(`(?m)^\s*goose version:\s*v`), Verified: true},
		Assets: gooseAssets,
		Install: func(home, hooksDir string) ([]string, error) {
			return install(home, gooseSteps(hooksDir)...)
		},
		SkillPath: agentsSkill,
		Status: func(home string) (bool, string) {
			return statusOf(home, gooseDir+"hooks/hooks.json", gooseSteps("")...)
		},
		Snippet: func(hooksDir string) string {
			return snippetOf(gooseAssets, hooksDir, "goose/hooks/hooks.json")
		},
		Events: []string{"done", "tool_use"},
		Flags:  gooseFlags,
	}
}

// gooseFlags is how Goose's flags are read: a launch is its `session`
// sub-command (the catalog's session recipe), whose flags `goose session
// --help` prints in clap's form (1.54.0, and the recipes tier). Its yolo
// recipe is a variable, so no flag of it is left to the switch.
var gooseFlags = &FlagsRecipe{
	Sub:  []string{"session"},
	Args: []string{"--help"},
}
