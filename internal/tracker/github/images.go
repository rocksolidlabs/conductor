package github

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// imagesOf finds an item's images: the <img src> of its body_html, at most
// tracker.MaxImages, each kept only when tracker.ImageHost admits it, one
// per ID, and counts the ones left out (on a host the rule does not admit,
// past the bound). A private repository's images are reachable only
// through the short-lived signed URLs GitHub puts in the rendered body,
// which is why the rendered body is read and not the Markdown.
func imagesOf(host, bodyHTML string) ([]tracker.ImageRef, tracker.ImagesLeftOut) {
	var out []tracker.ImageRef
	var left tracker.ImagesLeftOut
	seen := map[string]bool{}
	z := html.NewTokenizer(strings.NewReader(bodyHTML))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		if string(name) != "img" || !hasAttr {
			continue
		}
		var src, alt string
		for {
			k, v, more := z.TagAttr()
			switch string(k) {
			case "src":
				src = string(v)
			case "alt":
				alt = string(v)
			}
			if !more {
				break
			}
		}
		u, err := url.Parse(src)
		if err != nil || !u.IsAbs() || !tracker.ImageHost(tracker.GitHub, host, u) {
			left.Unlisted++
			continue
		}
		img := tracker.NewImageRef(u, cut(alt, 1024))
		if seen[img.ID] {
			continue
		}
		seen[img.ID] = true
		if len(out) == tracker.MaxImages {
			left.PastBound++
			continue
		}
		out = append(out, img)
	}
	return out, left
}

// guard is the base transport of the signed-address client: it sends a
// request only when admit admits its URL, checked at every hop, so the
// host judged is the host dialled whatever a redirect says, and sends it
// with no Referer, so a signed address's query never leaves the host it
// was signed for.
type guard struct {
	base  http.RoundTripper
	admit func(*url.URL) bool
}

func (g guard) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL == nil || !g.admit(r.URL) || (r.Host != "" && r.Host != r.URL.Host) {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, errRedirect
	}
	if r.Header.Get("Referer") != "" {
		r = r.Clone(r.Context())
		r.Header.Del("Referer")
	}
	return g.base.RoundTrip(r)
}

// signedClient is the client of an address the API named on another host:
// the base transport behind guard, no credential, a redirect followed only
// to a host admit admits, with no Referer.
func (a *Adapter) signedClient(admit func(*url.URL) bool) *http.Client {
	return &http.Client{
		Transport: guard{base: a.base, admit: admit},
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			next.Header.Del("Referer")
			if len(via) >= MaxRedirects || next.URL.Scheme != via[0].URL.Scheme || !admit(next.URL) {
				return errRedirect
			}
			return nil
		},
	}
}

// signedRefusal is a refusal from a host an address was signed for (an
// image's, a log's): that fetch's own outcome, its status alone. It is
// never the token's state (the host never saw the token: no
// tracker.ErrUnauthorized, ErrForbidden or ErrRateLimited), and no header
// or body text of that host reaches it.
func signedRefusal(path string, status int, what string) error {
	return &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Status: status, Err: tracker.ErrUnavailable, Words: "the " + what + " host refused it"}
}

// Image downloads one of a read's images from its signed address with no
// credential, at most max bytes (tracker.ErrBound past them), within
// ImageTimeout until its body is closed, and gives its media type as
// GitHub's answer says it.
func (a *Adapter) Image(ctx context.Context, c tracker.Credential, img tracker.ImageRef, max int64) (io.ReadCloser, string, error) {
	host := c.Host()
	if _, err := a.endpointFor(c, host); err != nil {
		return nil, "", err
	}
	u, err := url.Parse(img.FetchURL())
	admit := func(u *url.URL) bool { return tracker.ImageHost(tracker.GitHub, host, u) }
	if err != nil || img.FetchURL() == "" || !admit(u) {
		return nil, "", &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: "-", Err: tracker.ErrUnsupported, Words: "an image Conductor does not fetch"}
	}
	path := u.Path
	rctx, done := a.deadline(ctx, ImageTimeout)
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	if err != nil || req.URL.Host != u.Host {
		done()
		return nil, "", &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Err: tracker.ErrUnsupported}
	}
	req.Header.Set("User-Agent", a.ua)
	req.Header.Set("Accept", "image/png, image/jpeg, image/gif, image/webp, */*;q=0.1")
	resp, err := a.signedClient(admit).Do(req)
	if err != nil {
		done()
		return nil, "", a.transportError(ctx, rctx, http.MethodGet, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer done()
		defer resp.Body.Close()
		if resp.StatusCode < 400 {
			return nil, "", &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Status: resp.StatusCode, Err: tracker.ErrUnsupported}
		}
		return nil, "", signedRefusal(path, resp.StatusCode, "image")
	}
	if resp.ContentLength > max {
		resp.Body.Close()
		done()
		return nil, "", &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Status: resp.StatusCode, Err: tracker.ErrBound, Words: "past " + strconv.FormatInt(max, 10) + " bytes"}
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return &bounded{rc: resp.Body, left: max, path: path, done: done}, mt, nil
}

// bounded reads a body to at most left bytes; one more is the error
// tracker.ErrBound.
type bounded struct {
	rc   io.ReadCloser
	left int64
	path string
	done func()
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, b.boundErr()
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	if int64(n) > b.left {
		n, b.left = int(b.left), -1
		return n, b.boundErr()
	}
	b.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: b.path, Err: tracker.ErrUnavailable, Words: "the download failed"}
	}
	return n, err
}

func (b *bounded) boundErr() error {
	return &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: b.path, Status: http.StatusOK, Err: tracker.ErrBound}
}

func (b *bounded) Close() error {
	err := b.rc.Close()
	b.done()
	return err
}
