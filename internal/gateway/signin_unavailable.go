package gateway

// A request that ends because a subscription's sign-in has to be made
// again — revoked, expired past a refresh, lapsed, signed out — is told to
// the agent as a standard 503 with one word for every account:
// "upstream service unavailable", never the account's name. magpie keeps
// what it knows: the routing still passes over the account on its sign-in
// (failAuth), the account is still marked lapsed, and Recent calls and the
// usage log keep the status and words of the failure (this wraps outside
// counted and headerLogged). Only the last thing the agent is told
// changes, so a group with a member that answers still answers.
//
// The route's own words are what is read (provider.SignInGone), since an
// agent holding a bad API key says other words and is told as before. A
// Codex backend request is left alone here: Codex's own sign-in's 401 is
// passed on for Codex to renew (codex_backend.go), and magpie's own
// account there is told the 503 where it is written.
//
// The words agreed on with Mibo's fleet (2026-11-23).

import (
	"context"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// signInKeep is a request whose status Codex reads itself, whatever it
// says: Codex's own sign-in on the backend, which Codex renews on a 401
// (codex_backend.go), keeps it as it came.
type signInKeep struct{ kept bool }

type signInKeepKey struct{}

// keepSignInStatus says this request's status is not to be rewritten: set
// on the backend's own sign-in's behalf.
func keepSignInStatus(ctx context.Context) {
	if k, ok := ctx.Value(signInKeepKey{}).(*signInKeep); ok {
		k.kept = true
	}
}

// signInUnavailableText is what an agent is told when its request ended on
// a sign-in that has to be made again.
const signInUnavailableText = "upstream service unavailable"

// signInUnavailable is next with a sign-in-gone failure told as a 503 in
// the agent's own error shape.
func signInUnavailable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto, ok := signInProto(r)
		if !ok || magpieAsks(r) || claudeSignIn(r) {
			// not an agent's error to rewrite: an admin path, another
			// magpie's request (its failAuth stays auth), or Claude Code's
			// own sign-in (keepsSignIn tells it a 502 already)
			next.ServeHTTP(w, r)
			return
		}
		keep := &signInKeep{}
		r = r.WithContext(context.WithValue(r.Context(), signInKeepKey{}, keep))
		sw := &signInGoneWriter{ResponseWriter: w, proto: proto, keep: keep}
		defer sw.release()
		next.ServeHTTP(sw, r)
	})
}

// signInProto is the API a request speaks, for the error's shape, and
// false for a path that isn't one of the agent APIs.
func signInProto(r *http.Request) (provider.Protocol, bool) {
	if r.Method != http.MethodPost {
		// a model list or a page, not a turn: its 401 is for the client's
		// own sign-in (Codex's list asks for one), not a reply to rewrite
		return "", false
	}
	p := r.URL.Path
	switch {
	case p == CodexPath || strings.HasPrefix(p, CodexPath+"/"):
		return provider.Responses, true
	case p == "/v1/messages" || p == "/messages":
		return provider.Anthropic, true
	case strings.HasPrefix(p, "/v1beta/"):
		return provider.Gemini, true
	case p == "/v1/responses" || p == "/responses":
		return provider.Responses, true
	case p == "/v1/chat/completions" || p == "/chat/completions":
		return provider.Chat, true
	}
	return "", false
}

// signInGoneWriter holds an error's status until its body, which is what
// says whether the request ended on a sign-in to make again; told so, the
// body is dropped and a 503 in the agent's shape takes its place.
type signInGoneWriter struct {
	http.ResponseWriter
	proto provider.Protocol
	keep  *signInKeep
	held  int  // an error's status, waiting for its body
	wrote bool // the status went
	told  bool // the 503 went: the rest of the answer is dropped
}

func (w *signInGoneWriter) WriteHeader(code int) {
	if w.wrote || w.held != 0 {
		return
	}
	if code >= http.StatusBadRequest {
		w.held = code
		return
	}
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *signInGoneWriter) Write(b []byte) (int, error) {
	n := len(b)
	switch {
	case w.told:
		return n, nil
	case w.held != 0:
		if n == 0 {
			return 0, nil
		}
		code := w.held
		w.held = 0
		if w.keep != nil && w.keep.kept || !provider.SignInGone(b) {
			w.wrote = true
			w.ResponseWriter.WriteHeader(code)
			if _, err := w.ResponseWriter.Write(b); err != nil {
				return 0, err
			}
			return n, nil
		}
		w.told = true
		w.tell()
		return n, nil
	}
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// tell writes the 503 for an error whose status was held: no length, no
// encoding and no Retry-After from the failure it replaces, since an agent
// told to wait would wait for an account only a sign-in mends.
func (w *signInGoneWriter) tell() {
	h := w.Header()
	h.Del("Content-Length")
	h.Del("Content-Encoding")
	h.Del("Content-Type")
	h.Del("Retry-After")
	w.wrote = true
	writeErrorSaid(w.ResponseWriter, w.proto, http.StatusServiceUnavailable, signInUnavailableText, "service_unavailable", nil)
}

func (w *signInGoneWriter) Flush() {
	if w.held != 0 {
		w.wrote = true
		w.ResponseWriter.WriteHeader(w.held)
		w.held = 0
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController and the websocket's Accept reach the
// connection's own writer (deadlines, Hijack).
func (w *signInGoneWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// release sends an error status that never got a body. Nothing says it is
// a sign-in's, so it goes as it is.
func (w *signInGoneWriter) release() {
	if w.wrote || w.held == 0 {
		return
	}
	code := w.held
	w.held = 0
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}
