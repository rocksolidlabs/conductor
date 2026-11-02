package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A server session's link viewer whose credential no longer stands, its
// connection not closed for it (yet), has what it sends refused with the
// link's code and words, nothing done: its keys (revoked or expired, the
// program typed nothing), a read and a save (a FILE error), a chat post
// (an error naming its nonce). The connection stays.
func TestServerViewerRefusedOnceItsCredentialGoes(t *testing.T) {
	for _, c := range []struct {
		why   error
		code  string
		words string
	}{
		{session.ErrRevoked, proto.ErrCodeRevoked, "this link no longer opens the session"},
		{session.ErrExpired, proto.ErrCodeExpired, "this link has expired"},
	} {
		t.Run(c.code, func(t *testing.T) {
			e := newTestEnv(t, nil)
			id := e.createSession("cat")
			d, ok := e.srv.registry.Get(id)
			if !ok {
				t.Fatal("no session")
			}
			local := d.(*session.Local)
			file := filepath.Join(local.Info().Cwd, "notes.txt")
			if err := os.WriteFile(file, []byte("alpha\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// The link's check as the server makes it (attachCheck), its
			// answer the test's: the link standing until gone is set.
			var gone atomic.Bool
			check := func() (session.Grant, error) {
				if gone.Load() {
					return session.Grant{}, c.why
				}
				return session.Grant{Role: session.RoleControl}, nil
			}
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				conn.SetReadLimit(readLimit)
				e.srv.serveLocalViewer(r.Context(), conn, nil, local, session.RoleControl, "l-test", false, check, nil)
			}))
			t.Cleanup(hs.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			conn.SetReadLimit(proto.MaxFrame)
			t.Cleanup(func() { conn.CloseNow() })
			w := &wsClient{t: t, c: conn}
			w.hello(80, 24)
			w.expectControl(proto.CtlReady)
			w.send(proto.Encode(proto.TypeInput, []byte("before\n")))
			w.expectOutput("before")

			gone.Store(true)
			w.send(proto.Encode(proto.TypeInput, []byte("after\n")))
			if m := w.expectControl(proto.CtlError); m["code"] != c.code || m["message"] != c.words {
				t.Fatalf("the keys: %v", m)
			}
			w.send(proto.MustControl(proto.FileGet{T: proto.CtlFileGet, ReqID: "r1", Path: "notes.txt"}))
			if h := expectFileAnswer(t, w, "r1"); h.Kind != "error" || h.Error.Code != c.code || h.Error.Message != c.words {
				t.Fatalf("the read: %+v %+v", h, h.Error)
			}
			save, err := proto.EncodeFileWrite(proto.FileWrite{ReqID: "w1", Path: "notes.txt", Total: 4, Force: true}, []byte("mine"))
			if err != nil {
				t.Fatal(err)
			}
			w.send(save)
			if h := expectFileAnswer(t, w, "w1"); h.Kind != "error" || h.Error.Code != c.code || h.Error.Message != c.words {
				t.Fatalf("the save: %+v %+v", h, h.Error)
			}
			w.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Nonce: "c1", Text: "after"}))
			if m := w.expectControl(proto.CtlError); m["code"] != c.code || m["message"] != c.words || m["requestId"] != "c1" {
				t.Fatalf("the chat post: %v", m)
			}
			// Still open, and nothing was typed: the next ping is answered
			// with no output before it.
			w.send(proto.MustControl(proto.Ping{T: proto.CtlPing, TS: 7}))
			for {
				f, err := w.read()
				if err != nil {
					t.Fatalf("the connection: %v", err)
				}
				if f.Type == proto.TypeOutput && strings.Contains(string(f.Payload), "after") {
					t.Fatalf("the keys were typed: %q", f.Payload)
				}
				if f.Type == proto.TypeControl {
					var m map[string]any
					if json.Unmarshal(f.Payload, &m) == nil && m["t"] == proto.CtlPong {
						break
					}
				}
			}
			if b, _ := os.ReadFile(file); string(b) != "alpha\n" {
				t.Fatalf("the file is %q", b)
			}
		})
	}
}

// expectFileAnswer reads w until the FILE frame answering reqID.
func expectFileAnswer(t *testing.T, w *wsClient, reqID string) proto.FileHeader {
	t.Helper()
	for {
		f, err := w.read()
		if err != nil {
			t.Fatalf("waiting for the answer to %s: %v", reqID, err)
		}
		if f.Type != proto.TypeFile {
			continue
		}
		if h, _, err := proto.DecodeFile(f.Payload); err == nil && h.ReqID == reqID {
			return h
		}
	}
}
