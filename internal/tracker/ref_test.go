package tracker

import (
	"errors"
	"testing"
)

func TestParseGitHubURLs(t *testing.T) {
	o := ParseOptions{Hosts: []string{"ghe.example.test"}}
	lo := ParseOptions{Loopback: true}
	ok := []struct {
		raw, def string
		o        ParseOptions
		want     Ref
	}{
		{"https://github.com/octo-example/widgets/issues/42", "", o, Ref{GitHub, "github.com", "octo-example/widgets", KindIssue, "42"}},
		{"https://github.com/octo-example/widgets/pull/43", "", o, Ref{GitHub, "github.com", "octo-example/widgets", KindChange, "43"}},
		{"  https://GitHub.com/Octo-Example/Widgets.js/pull/43  ", "", o, Ref{GitHub, "github.com", "Octo-Example/Widgets.js", KindChange, "43"}},
		{"octo-example/widgets#12", "", o, Ref{GitHub, "github.com", "octo-example/widgets", KindIssue, "12"}},
		{"#12", "octo-example/widgets", o, Ref{GitHub, "github.com", "octo-example/widgets", KindIssue, "12"}},
		{"#12", "https://github.com/octo-example/widgets", o, Ref{GitHub, "github.com", "octo-example/widgets", KindIssue, "12"}},
		{"#12", "https://github.com/octo-example/widgets.git/", o, Ref{GitHub, "github.com", "octo-example/widgets", KindIssue, "12"}},
		{"#12", "git@github.com:octo-example/widgets.git", o, Ref{GitHub, "github.com", "octo-example/widgets", KindIssue, "12"}},
		{"#12", "ssh://git@github.com:22/octo-example/widgets.git", o, Ref{GitHub, "github.com", "octo-example/widgets", KindIssue, "12"}},
		{"#12", "git@ghe.example.test:team/tool.git", o, Ref{GitHub, "ghe.example.test", "team/tool", KindIssue, "12"}},
		{"team/other#3", "git@ghe.example.test:team/tool.git", o, Ref{GitHub, "ghe.example.test", "team/other", KindIssue, "3"}},
		{"https://ghe.example.test/team/tool/issues/9", "", o, Ref{GitHub, "ghe.example.test", "team/tool", KindIssue, "9"}},
		{"http://127.0.0.1:8123/team/tool/issues/9", "", lo, Ref{GitHub, "127.0.0.1:8123", "team/tool", KindIssue, "9"}},
		{"https://github.com/octo-example/widgets/issues/18446744073709551615", "", o, Ref{GitHub, "github.com", "octo-example/widgets", KindIssue, "18446744073709551615"}},
	}
	for _, c := range ok {
		got, err := ParseGitHub(c.raw, c.def, c.o)
		if err != nil || got != c.want {
			t.Errorf("ParseGitHub(%q, %q) = %+v, %v; want %+v", c.raw, c.def, got, err, c.want)
		}
	}
	refused := []struct {
		raw, def string
		o        ParseOptions
	}{
		{"https://gitlab.example/octo/widgets/issues/1", "", o},
		{"https://ghe.other.test/team/tool/issues/9", "", o},
		{"https://github.com/octo-example/widgets/issues/42?x=1", "", o},
		{"https://github.com/octo-example/widgets/issues/42?", "", o},
		{"https://github.com/octo-example/widgets/issues/42#issuecomment-1", "", o},
		{"https://github.com/octo-example/widgets/issues/42/files", "", o},
		{"https://github.com/octo-example/widgets/pulls/42", "", o},
		{"https://github.com/octo-example/widgets/issues/042", "", o},
		{"https://github.com/octo-example/widgets/issues/0", "", o},
		{"https://github.com/octo-example/widgets/issues/1x", "", o},
		{"https://github.com/octo-example/widgets/issues/123456789012345678901", "", o},
		{"https://github.com:8443/octo-example/widgets/issues/42", "", o},
		{"https://user@github.com/octo-example/widgets/issues/42", "", o},
		{"http://github.com/octo-example/widgets/issues/42", "", o},
		{"http://127.0.0.1:8123/team/tool/issues/9", "", o},
		{"http://ghe.example.test/team/tool/issues/9", "", ParseOptions{Hosts: []string{"ghe.example.test"}, Loopback: true}},
		{"ftp://github.com/octo-example/widgets/issues/42", "", o},
		{"https://github.com/octo_example/widgets/issues/42", "", o},
		{"https://github.com/octo-example/../issues/42", "", o},
		{"#12", "", o},
		{"12", "octo-example/widgets", o},
		{"octo-example#12", "", o},
		{"", "", o},
	}
	for _, c := range refused {
		if got, err := ParseGitHub(c.raw, c.def, c.o); !errors.Is(err, ErrRef) {
			t.Errorf("ParseGitHub(%q, %q) = %+v, %v; want ErrRef", c.raw, c.def, got, err)
		}
	}
}

func TestRefStrings(t *testing.T) {
	r := Ref{GitHub, "github.com", "octo-example/widgets", KindChange, "43"}
	if r.String() != "octo-example/widgets#43" || r.Key() != "github:github.com/octo-example/widgets/change/43" {
		t.Fatalf("String %q Key %q", r.String(), r.Key())
	}
}
