package agents

import "testing"

// Each adapter that takes a model says which arguments choose one: every
// form its tool takes, and nothing else.
func TestChoosesModel(t *testing.T) {
	for _, a := range All() {
		if (a.ModelArgs == nil) != (a.ChoosesModel == nil) {
			t.Errorf("%s: ModelArgs and ChoosesModel are set apart", a.ID)
		}
	}
	for id, cases := range map[string]map[bool][][]string{
		"claude": {
			true:  {{"--model", "opus"}, {"--verbose", "--model=opus"}},
			false: {{"-m", "opus"}, {"--models"}, {}},
		},
		"codex": {
			true: {{"--model", "o3"}, {"--model=o3"}, {"-m", "o3"}, {"-mo3"}, {"-c", "model=o3"}, {"-c", ` model = "o3"`},
				{"--config", "model=o3"}, {"--config=model=o3"}, {"-cmodel=o3"}, {"-c=model=o3"}},
			false: {{"-c", "model_reasoning_effort=high"}, {"-c", "mcp_servers.conductor.command=x"}, {"--sandbox", "read-only"}, {"--config", "model"}, {"--models"}, {}},
		},
	} {
		a, _ := Get(id)
		for want, lines := range cases {
			for _, line := range lines {
				if got := a.ChoosesModel(line); got != want {
					t.Errorf("%s %q: %v, want %v", id, line, got, want)
				}
			}
		}
	}
}
