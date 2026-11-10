package tracker

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// ImageRef is an image a read found in an item's body: ID its URL with the
// query and the fragment removed (what is stored and shown: a signed URL's
// query is a credential and is never stored or logged), Alt its text, and
// the URL to download it now, held in memory only for the read that found
// it (FetchURL). Its fmt forms, its slog value and its JSON say the ID
// alone.
type ImageRef struct {
	ID    string
	Alt   string
	// fetch is behind a closure: fmt and reflection print a func as its
	// address, so no print reaches the signed query.
	fetch func() string
}

// NewImageRef makes the reference of an image to fetch at u.
func NewImageRef(u *url.URL, alt string) ImageRef {
	id := *u
	id.RawQuery, id.ForceQuery, id.Fragment, id.RawFragment, id.User = "", false, "", "", nil
	fetch := u.String()
	return ImageRef{ID: id.String(), Alt: alt, fetch: func() string { return fetch }}
}

// FetchURL is the URL to download the image now (” for a reference not
// made by a read, such as one read back from a store).
func (i ImageRef) FetchURL() string {
	if i.fetch == nil {
		return ""
	}
	return i.fetch()
}

// String is the ID.
func (i ImageRef) String() string { return "image(" + i.ID + ")" }

// GoString is the String, for %#v.
func (i ImageRef) GoString() string { return i.String() }

// Format writes the String for every verb.
func (i ImageRef) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(i.String())) }

// LogValue is the ID, for slog.
func (i ImageRef) LogValue() slog.Value { return slog.StringValue(i.ID) }

// MarshalJSON writes the ID and the alt text, never the URL to fetch.
func (i ImageRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID  string `json:"id"`
		Alt string `json:"alt,omitempty"`
	}{i.ID, i.Alt})
}

// githubImageHosts are the hosts github.com serves an issue's images from
// (beside github.com itself under /user-attachments/assets/).
var githubImageHosts = map[string]bool{
	"private-user-images.githubusercontent.com": true,
	"user-images.githubusercontent.com":         true,
	"camo.githubusercontent.com":                true,
	"objects.githubusercontent.com":             true,
}

// githubLogSuffixes are the hosts github.com keeps GitHub Actions' job
// logs on (GitHub's list of domains by function, Actions' logs).
var githubLogSuffixes = []string{".actions.githubusercontent.com", ".blob.core.windows.net"}

// ImageHost reports whether an image may be downloaded from u for an item
// of system on host: https only, no user; for github.com the hosts GitHub
// serves issue images from and github.com under /user-attachments/assets/;
// for a GitHub Enterprise Server host, that host and media.<host>. A
// redirect is followed only to such a host. No other host is ever fetched,
// so an item's text cannot make the server request an address of its
// network.
func ImageHost(system, host string, u *url.URL) bool {
	if system != GitHub || u == nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return false
	}
	h := strings.ToLower(u.Host)
	if host == GitHubHost {
		if githubImageHosts[h] {
			return true
		}
		return h == GitHubHost && strings.HasPrefix(u.EscapedPath(), "/user-attachments/assets/") && !strings.Contains(u.Path, "..")
	}
	return host != "" && (h == host || h == "media."+host)
}

// LogHost reports whether a GitHub Actions job's log may be fetched from u
// for an item of system on host, used only for the redirect a job's logs
// route answers (a URL the authenticated API gave, never one a body names)
// and never for an image: https only, no user; for github.com the hosts
// GitHub keeps Actions' logs on (githubLogSuffixes); for a GitHub
// Enterprise Server host, that host and its subdomains only.
func LogHost(system, host string, u *url.URL) bool {
	if system != GitHub || u == nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return false
	}
	h := strings.ToLower(u.Host)
	if host == GitHubHost {
		for _, s := range githubLogSuffixes {
			if strings.HasSuffix(h, s) && len(h) > len(s) && !strings.Contains(h, ":") {
				return true
			}
		}
		return false
	}
	return host != "" && (h == host || strings.HasSuffix(h, "."+host))
}
