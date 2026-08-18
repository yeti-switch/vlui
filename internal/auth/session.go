package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// User is who is logged in. It is what the session cookie carries and what
// /api/auth/me returns.
type User struct {
	Subject string   `json:"sub"`
	Email   string   `json:"email,omitempty"`
	Name    string   `json:"name,omitempty"`
	Groups  []string `json:"groups,omitempty"`
}

// session is the cookie payload: the user, plus when it stops being valid.
type session struct {
	User
	Expires int64 `json:"exp"` // unix seconds

	// IDToken is kept only when logout is "provider": RP-initiated logout needs
	// it as id_token_hint, which is how the provider knows whose session to end
	// (and, for most providers, is what stops the endpoint being an open logout
	// CSRF). It is a ~1KB JWT, so it is not carried when nothing needs it.
	IDToken string `json:"idt,omitempty"`
}

// The cookie is signed, not encrypted: it carries a name and an email, which
// the browser's own user already knows. Signing is what matters — it stops the
// user editing their own groups claim and granting themselves access.
//
// Stateless by choice. A server-side session table would be a second source of
// truth to keep, back up and expire — and this application deliberately has no
// database at all;
// revoking everything at once is done by rotating cookie_secret.
func (a *Auth) encode(s session) (string, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + a.sign(body), nil
}

func (a *Auth) decode(token string) (session, error) {
	var s session

	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return s, errors.New("malformed session cookie")
	}
	// Constant time: a timing oracle on the signature is a forgery oracle.
	if !hmac.Equal([]byte(sig), []byte(a.sign(body))) {
		return s, errors.New("bad session signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return s, fmt.Errorf("bad session encoding: %w", err)
	}
	if err := json.Unmarshal(payload, &s); err != nil {
		return s, fmt.Errorf("bad session payload: %w", err)
	}
	// Checked after the signature, so an expired-but-forged cookie still fails
	// as a forgery.
	if time.Now().Unix() >= s.Expires {
		return s, errors.New("session expired")
	}
	return s, nil
}

func (a *Auth) sign(body string) string {
	m := hmac.New(sha256.New, []byte(a.cfg.CookieSecret))
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *Auth) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:  name,
		Value: value,
		Path:  a.cookiePath(),
		// The browser must never hand this to JavaScript: an XSS anywhere in the
		// SPA would otherwise be a session theft.
		HttpOnly: true,
		Secure:   *a.cfg.SecureCookie,
		// Lax, not Strict: the login flow returns via a cross-site redirect from
		// the IdP, and Strict would withhold the cookie on that navigation and
		// loop the user back to the provider forever.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func (a *Auth) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     a.cookiePath(),
		HttpOnly: true,
		Secure:   *a.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// The cookie is scoped to the mount point, so an app sharing the domain at a
// different path never receives it.
func (a *Auth) cookiePath() string {
	if a.base == "" {
		return "/"
	}
	return a.base + "/"
}
