package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A request that ends on a sign-in to make again is told the standard 503
// in the agent's own error shape, never the account's words; anything else
// — an API key's 401, a served reply, a Codex backend request, another
// magpie's — goes as it was.
func TestSignInUnavailableTold(t *testing.T) {
	revoked := `{"error":{"message":"me@example.com's sign-in was revoked; sign in again","type":"authentication_error","code":"token_revoked"}}`
	badKey := `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`
	send := func(path, ua string, status int, body string, ownSignIn bool) *httptest.ResponseRecorder {
		t.Helper()
		h := signInUnavailable(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ownSignIn {
				// the Codex backend's own sign-in, which Codex renews
				keepSignInStatus(r.Context())
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(status)
			io.WriteString(w, body)
		}))
		req := httptest.NewRequest("POST", path, nil)
		if ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, c := range []struct {
		name, path, ua string
		status         int
		body           string
		ownSignIn      bool
		want           int
		said           string
	}{
		{"revoked, chat", "/v1/chat/completions", "", 401, revoked, false, 503, signInUnavailableText},
		{"revoked, responses", "/v1/responses", "", 401, revoked, false, 503, signInUnavailableText},
		{"a sign error, chat", "/v1/chat/completions", "", 502, `{"error":{"message":"Fake: me@example.com's sign-in has expired — sign in again","type":"api_error"}}`, false, 503, signInUnavailableText},
		{"an API key, chat", "/v1/chat/completions", "", 401, badKey, false, 401, "Incorrect API key"},
		{"an API key, messages", "/v1/messages", "", 401, badKey, false, 401, "Incorrect API key"},
		{"a served reply", "/v1/chat/completions", "", 200, `{"id":"x"}`, false, 200, `{"id":"x"}`},
		{"Codex's own path", CodexPath + "/responses", "", 401, revoked, false, 503, signInUnavailableText},
		{"Codex's own sign-in", CodexPath + "/responses", "", 401, revoked, true, 401, "sign-in was revoked"},
		{"another magpie", "/v1/chat/completions", "magpie/0.1.4732", 401, revoked, false, 401, "sign-in was revoked"},
		{"not an agent API", "/v1/magpie/usage", "", 401, revoked, false, 401, "sign-in was revoked"},
	} {
		rec := send(c.path, c.ua, c.status, c.body, c.ownSignIn)
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.said) {
			t.Errorf("%s: %d %s, want %d saying %q", c.name, rec.Code, rec.Body.String(), c.want, c.said)
		}
		if c.want == 503 && rec.Header().Get("Retry-After") != "" {
			t.Errorf("%s: told to wait %q for an account only a sign-in mends", c.name, rec.Header().Get("Retry-After"))
		}
	}
}

// The 503 is written in the API's own shape, without the account's name.
func TestSignInUnavailableShape(t *testing.T) {
	revoked := `{"error":{"message":"me@example.com's sign-in was revoked; sign in again","type":"authentication_error","code":"token_revoked"}}`
	send := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		h := signInUnavailable(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			io.WriteString(w, revoked)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
		return rec
	}
	if rec := send("/v1/messages"); !strings.Contains(rec.Body.String(), `"type":"error"`) || !strings.Contains(rec.Body.String(), `"type":"api_error"`) {
		t.Errorf("messages: %d %s", rec.Code, rec.Body.String())
	}
	rec := send("/v1beta/models/gemini-3:generateContent")
	var g struct {
		Error struct {
			Code    int
			Message string
			Status  string
		}
	}
	if json.Unmarshal(rec.Body.Bytes(), &g) != nil || g.Error.Code != 503 || g.Error.Status != "UNAVAILABLE" || g.Error.Message != signInUnavailableText {
		t.Errorf("gemini: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1beta/models/x:generateContent"} {
		if got := send(path).Body.String(); strings.Contains(got, "example.com") {
			t.Errorf("%s: told the account's name: %s", path, got)
		}
	}
}

// Every account of a provider whose sign-in is revoked ends the request on
// the standard 503, while Recent calls keeps the 401 the vendor gave.
func TestSignInGoneEveryAccountToldUnavailable(t *testing.T) {
	codexSignedIn(t)
	revokedUpstream(t, codexAccessToken(t))
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.6-sol"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"hi"}`))
	req.Header.Set("User-Agent", "codex-cli/0.60.0")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), signInUnavailableText) {
		t.Fatalf("%d %s, want 503 %q", rec.Code, rec.Body.String(), signInUnavailableText)
	}
	if strings.Contains(rec.Body.String(), "example.com") {
		t.Fatalf("told the account's name: %s", rec.Body.String())
	}
	if r := s.Recent(); len(r) == 0 || r[0].Status != 401 {
		t.Fatalf("Recent calls: %+v, want the vendor's 401 kept", r)
	}
}
