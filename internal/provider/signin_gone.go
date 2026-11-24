package provider

import (
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// SignInGone says a reply's words are that the sign-in it was made with has
// to be made again: revoked, expired past a refresh, lapsed or signed out,
// whatever status the reply came with. It is asked of what reaches an agent
// at the end of a request, so one whose accounts are all so may be told
// something standard instead (gateway/signin_unavailable.go).
//
// The words are the ones magpie itself says of a lapsed account
// (revokedText, copilotLapsedText, ErrSIWCSignIn, "Codex is signed out"),
// the vendors' own for it (revokedWords, ClaudeSignInRequired), and a
// plugin's usage read (signInGone). A refused API key says none of them.
func SignInGone(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return revokedWords.Match(body) || signInGone.Match(body) || signedOut.Match(body) ||
		ClaudeSignInRequired(string(body)) || strings.Contains(string(body), copilotLapsedText)
}

// signedOut is the rest of the words magpie and the agents say of a sign-in
// that has to be made again.
var signedOut = lazyre.New(`(?i)\bsigned out\b|sign-in required|run codex login|sign in again from magpie|sign in to (?:this account|copilot) again`)
