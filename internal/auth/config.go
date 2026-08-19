// Package auth implements OIDC login (authorization code + PKCE) and a signed
// session cookie.
//
// Provider-agnostic by design: everything is learned from the issuer's discovery
// document, so Keycloak, Authentik, Okta, Google, Entra — anything conformant —
// works with only an issuer URL and a client id. Endpoints can be given by hand
// for a provider that publishes no discovery document.
//
// There is no session store. The cookie is signed and carries the user itself,
// which is what lets this application keep its promise of having no database.
package auth

import (
	"fmt"
	"time"
)

type Config struct {
	// Enabled turns authentication on. Off means every request is anonymous —
	// acceptable only behind an IP allowlist or on a trusted network.
	Enabled bool `yaml:"enabled"`

	// Issuer is the OIDC issuer URL, e.g. https://keycloak.example/realms/yeti.
	// Its /.well-known/openid-configuration supplies everything else.
	Issuer string `yaml:"issuer"`

	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`

	// RedirectURL must be registered with the provider and must match exactly,
	// including the base path: https://logs.example.com/logs/api/auth/callback
	RedirectURL string `yaml:"redirect_url"`

	// Scopes defaults to openid, profile, email.
	Scopes []string `yaml:"scopes"`

	// Endpoints override discovery, for a provider that publishes no
	// /.well-known/openid-configuration. Leave empty to discover.
	AuthURL     string `yaml:"auth_url"`
	TokenURL    string `yaml:"token_url"`
	JWKSURL     string `yaml:"jwks_url"`
	UserInfoURL string `yaml:"userinfo_url"`

	// CookieSecret signs the session cookie. At least 32 bytes. Rotating it
	// logs everyone out, which is also how you revoke every session at once.
	CookieSecret string `yaml:"cookie_secret"`
	CookieName   string `yaml:"cookie_name"`

	// SecureCookie marks the cookie Secure. Leave true; set false only to test
	// over plain HTTP on localhost.
	SecureCookie *bool `yaml:"secure_cookie"`

	// SessionTTL is how long a login lasts before the user is sent back to the
	// provider. There is no refresh-token flow: this is a log viewer, and a
	// silent re-auth against a live IdP session is cheap.
	SessionTTL time.Duration `yaml:"session_ttl"`

	// Logout decides what "Sign out" means:
	//
	//   local    (default) — drop our session only. The IdP session survives, so
	//                        signing in again is a click with no password
	//                        prompt. That is the SSO contract, and it is what
	//                        most apps do: it does not touch any other app
	//                        sharing the issuer.
	//
	//   provider           — RP-initiated logout: also end the IdP session, via
	//                        end_session_endpoint. This signs the user out of
	//                        EVERY app on that IdP. That is a large blast radius
	//                        for a button in a log viewer, so it is opt-in.
	Logout string `yaml:"logout"`

	// PostLogoutRedirectURL is where the provider returns the user after an
	// RP-initiated logout. It must be registered with the provider. Only used
	// when logout is "provider".
	PostLogoutRedirectURL string `yaml:"post_logout_redirect_url"`

	// AllowedGroups, when set, requires the ID token to carry one of these in
	// GroupsClaim. Authorisation, as opposed to authentication: without it,
	// anyone the IdP knows can read every log line this tenant holds.
	AllowedGroups []string `yaml:"allowed_groups"`
	GroupsClaim   string   `yaml:"groups_claim"`
}

// Validate reports whether this configuration could start, without contacting
// the provider. It is what `vlui -check-config` calls: the deeper checks live
// in New, which also performs OIDC discovery, and a config check that needed a
// reachable IdP would be useless in exactly the moment you want it — before a
// restart, or in CI.
//
// The receiver is a copy, so filling in defaults here cannot affect the caller.
func (c Config) Validate() error {
	c.applyDefaults()
	return c.validate()
}

func (c *Config) applyDefaults() {
	if len(c.Scopes) == 0 {
		c.Scopes = []string{"openid", "profile", "email"}
	}
	if c.CookieName == "" {
		c.CookieName = "vlui_session"
	}
	if c.SessionTTL == 0 {
		c.SessionTTL = 12 * time.Hour
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = "groups"
	}
	if c.Logout == "" {
		c.Logout = LogoutLocal
	}
	if c.SecureCookie == nil {
		t := true
		c.SecureCookie = &t
	}
}

func (c *Config) validate() error {
	if c.Issuer == "" && c.AuthURL == "" {
		return fmt.Errorf("auth: issuer is required (or give auth_url/token_url/jwks_url by hand)")
	}
	if c.ClientID == "" {
		return fmt.Errorf("auth: client_id is required")
	}
	if c.RedirectURL == "" {
		return fmt.Errorf("auth: redirect_url is required and must match what the provider has registered")
	}
	// A short secret is worse than none, because it looks like security.
	if len(c.CookieSecret) < 32 {
		return fmt.Errorf("auth: cookie_secret must be at least 32 bytes (got %d)", len(c.CookieSecret))
	}
	if c.Logout != LogoutLocal && c.Logout != LogoutProvider {
		return fmt.Errorf("auth: logout must be %q or %q, got %q",
			LogoutLocal, LogoutProvider, c.Logout)
	}
	return nil
}

const (
	LogoutLocal    = "local"
	LogoutProvider = "provider"
)
