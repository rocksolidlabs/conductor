package tracker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestImageHost(t *testing.T) {
	admitted := []struct{ host, u string }{
		{"github.com", "https://private-user-images.githubusercontent.com/1/2-uuid.png?jwt=x"},
		{"github.com", "https://user-images.githubusercontent.com/1/2.png"},
		{"github.com", "https://camo.githubusercontent.com/abc/def"},
		{"github.com", "https://objects.githubusercontent.com/github-production-user-asset/1"},
		{"github.com", "https://github.com/user-attachments/assets/6f1c2e9a-4b7d-4a52-9c3e-0f1d2a3b4c5d"},
		{"github.com", "https://GitHub.com/user-attachments/assets/x"},
		{"ghe.example.test", "https://ghe.example.test/user-attachments/assets/x"},
		{"ghe.example.test", "https://media.ghe.example.test/user/1/files/x.png"},
	}
	for _, c := range admitted {
		if !ImageHost(GitHub, c.host, mustURL(t, c.u)) {
			t.Errorf("ImageHost(%s, %s) refused", c.host, c.u)
		}
	}
	refused := []struct{ system, host, u string }{
		{GitHub, "github.com", "http://user-images.githubusercontent.com/1/2.png"},
		{GitHub, "github.com", "https://user-images.githubusercontent.com.evil.example/1.png"},
		{GitHub, "github.com", "https://evilgithubusercontent.com/1.png"},
		{GitHub, "github.com", "https://githubusercontent.com/1.png"},
		{GitHub, "github.com", "https://raw.githubusercontent.com/o/r/main/x.png"},
		{GitHub, "github.com", "https://github.com/octo-example/widgets/raw/main/x.png"},
		{GitHub, "github.com", "https://github.com/user-attachments/files/1/x.zip"},
		{GitHub, "github.com", "https://github.com/user-attachments/assets/../../octo/repo"},
		{GitHub, "github.com", "https://user@camo.githubusercontent.com/abc"},
		{GitHub, "github.com", "https://camo.githubusercontent.com:8443/abc"},
		{GitHub, "github.com", "https://ghe.example.test/user-attachments/assets/x"},
		{GitHub, "github.com", "https://169.254.169.254/latest/meta-data"},
		{GitHub, "ghe.example.test", "https://github.com/user-attachments/assets/x"},
		{GitHub, "ghe.example.test", "https://other.ghe.example.test/x.png"},
		{GitHub, "ghe.example.test", "http://ghe.example.test/x.png"},
		{GitHub, "ghe.example.test", "https://ghe.example.test.evil.example/x.png"},
		{"gitlab", "github.com", "https://camo.githubusercontent.com/abc"},
	}
	for _, c := range refused {
		if ImageHost(c.system, c.host, mustURL(t, c.u)) {
			t.Errorf("ImageHost(%s, %s, %s) admitted", c.system, c.host, c.u)
		}
	}
	if ImageHost(GitHub, "github.com", nil) {
		t.Error("a nil URL admitted")
	}
}

func TestLogHost(t *testing.T) {
	logs := []string{
		"https://productionresultssa0.blob.core.windows.net/actions-results/x/logs/job/job-logs.txt?sig=s",
		"https://pipelines.actions.githubusercontent.com/serviceHosts/x/_apis/pipelines/1/runs/2/signedlogcontent/3",
		"https://results-receiver.actions.githubusercontent.com/x",
	}
	for _, s := range logs {
		u := mustURL(t, s)
		if !LogHost(GitHub, "github.com", u) {
			t.Errorf("LogHost refused %s", s)
		}
		if ImageHost(GitHub, "github.com", u) {
			t.Errorf("ImageHost admitted the log host %s", s)
		}
	}
	for _, s := range []string{
		"https://camo.githubusercontent.com/abc",
		"https://private-user-images.githubusercontent.com/1/2.png",
		"https://github.com/user-attachments/assets/x",
	} {
		if LogHost(GitHub, "github.com", mustURL(t, s)) {
			t.Errorf("LogHost admitted the image host %s", s)
		}
	}
	for _, s := range []string{
		"http://productionresultssa0.blob.core.windows.net/x",
		"https://blob.core.windows.net/x",
		"https://.blob.core.windows.net/x",
		"https://evil.example/x.blob.core.windows.net",
		"https://blob.core.windows.net.evil.example/x",
		"https://productionresultssa0.blob.core.windows.net:8443/x",
		"https://user@pipelines.actions.githubusercontent.com/x",
	} {
		if LogHost(GitHub, "github.com", mustURL(t, s)) {
			t.Errorf("LogHost admitted %s", s)
		}
	}
	ghe := "ghe.example.test"
	for s, want := range map[string]bool{
		"https://ghe.example.test/_services/x/logs":          true,
		"https://storage.ghe.example.test/x":                 true,
		"https://productionresultssa0.blob.core.windows.net": false,
		"https://ghe.example.test.evil.example/x":            false,
		"http://ghe.example.test/x":                          false,
	} {
		if LogHost(GitHub, ghe, mustURL(t, s)) != want {
			t.Errorf("LogHost(%s, %s) want %v", ghe, s, want)
		}
	}
}

func TestImageRefNeverPrintsItsQuery(t *testing.T) {
	img := NewImageRef(mustURL(t, "https://private-user-images.githubusercontent.com/1/2-uuid.png?jwt=SECRETJWT#frag"), "shot")
	if img.ID != "https://private-user-images.githubusercontent.com/1/2-uuid.png" {
		t.Fatalf("ID %q", img.ID)
	}
	if !strings.Contains(img.FetchURL(), "jwt=SECRETJWT") {
		t.Fatalf("FetchURL %q", img.FetchURL())
	}
	b, _ := json.Marshal(Item{Images: []ImageRef{img}})
	type holder struct {
		inner ImageRef
		ptr   *ImageRef
	}
	var out []string
	for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		out = append(out, fmt.Sprintf(f, img), fmt.Sprintf(f, Item{Images: []ImageRef{img}}), fmt.Sprintf(f, holder{img, &img}))
	}
	out = append(out, string(b))
	for _, s := range out {
		if strings.Contains(s, "SECRETJWT") {
			t.Fatalf("the signed query printed: %s", s)
		}
	}
	var back Item
	if err := json.Unmarshal(b, &back); err != nil || back.Images[0].ID != img.ID || back.Images[0].Alt != "shot" || back.Images[0].FetchURL() != "" {
		t.Fatalf("read back %+v, %v", back.Images, err)
	}
}
