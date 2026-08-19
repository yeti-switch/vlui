package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const secret = "0123456789abcdef0123456789abcdef" // 32 bytes

func testAuth(t *testing.T, base string) *Auth {
	t.Helper()
	cfg := Config{
		Enabled:      true,
		Issuer:       "https://idp.example",
		ClientID:     "vlui",
		RedirectURL:  "https://logs.example/api/auth/callback",
		CookieSecret: secret,
	}
	cfg.applyDefaults()
	return &Auth{
		cfg:  cfg,
		base: base,
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestSessionRoundTrips(t *testing.T) {
	a := testAuth(t, "")
	want := User{Subject: "u1", Email: "op@example.com", Name: "Op", Groups: []string{"noc"}}

	tok, err := a.encode(session{User: want, Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.decode(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != want.Subject || got.Email != want.Email || len(got.Groups) != 1 {
		t.Errorf("session = %+v, want %+v", got.User, want)
	}
}

// The cookie is signed, not encrypted. Signing is what stops a user editing
// their own groups claim and granting themselves access.
func TestTamperedSessionIsRejected(t *testing.T) {
	a := testAuth(t, "")

	tok, err := a.encode(session{
		User:    User{Subject: "u1", Groups: []string{"readonly"}},
		Expires: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Rewrite the payload, keep the original signature.
	body, sig, _ := strings.Cut(tok, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(body)
	var s session
	_ = json.Unmarshal(raw, &s)
	s.Groups = []string{"admin"} // privilege escalation attempt
	evil, _ := json.Marshal(s)
	forged := base64.RawURLEncoding.EncodeToString(evil) + "." + sig

	if _, err := a.decode(forged); err == nil {
		t.Fatal("a re-written session payload must not verify")
	}
}

// A cookie signed with a different secret must not be accepted — that is what
// rotating cookie_secret relies on to revoke every session at once.
func TestSessionFromAnotherSecretIsRejected(t *testing.T) {
	a := testAuth(t, "")
	other := testAuth(t, "")
	other.cfg.CookieSecret = "ffffffffffffffffffffffffffffffff"

	tok, _ := other.encode(session{User: User{Subject: "u1"}, Expires: time.Now().Add(time.Hour).Unix()})
	if _, err := a.decode(tok); err == nil {
		t.Fatal("a session signed with another secret must not verify")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	a := testAuth(t, "")
	tok, _ := a.encode(session{
		User:    User{Subject: "u1"},
		Expires: time.Now().Add(-time.Minute).Unix(),
	})
	if _, err := a.decode(tok); err == nil {
		t.Fatal("an expired session must not verify")
	}
}

// --- middleware -------------------------------------------------------------

func protected(a *Auth) http.Handler {
	return a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFrom(r.Context())
		_, _ = w.Write([]byte(u.Subject))
	}))
}

func TestMiddlewareRejectsNoCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	protected(testAuth(t, "")).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/query", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// The SPA needs to know where to send the user.
	if !strings.Contains(rec.Body.String(), "login_url") {
		t.Errorf("401 should carry a login_url, got %s", rec.Body)
	}
}

func TestMiddlewarePassesValidSession(t *testing.T) {
	a := testAuth(t, "")
	tok, _ := a.encode(session{User: User{Subject: "u42"}, Expires: time.Now().Add(time.Hour).Unix()})

	r := httptest.NewRequest(http.MethodGet, "/api/query", nil)
	r.AddCookie(&http.Cookie{Name: a.cfg.CookieName, Value: tok})

	rec := httptest.NewRecorder()
	protected(a).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "u42" {
		t.Errorf("the handler should see the user, got %q", rec.Body)
	}
}

// Regression, caught only by driving a real login end to end: /auth/me is
// mounted OUTSIDE the auth middleware — it must be, or logging in would require
// being logged in, and it has to be able to answer "nobody". It therefore has to
// read the cookie itself. Reading the user from the request context made it
// answer 401 even for a perfectly valid session, so the SPA showed the sign-in
// screen to users who were already signed in.
func TestMeReadsTheCookieWithoutMiddleware(t *testing.T) {
	a := testAuth(t, "/stats")
	tok, _ := a.encode(session{
		User:    User{Subject: "u1", Email: "noc@yeti.example", Name: "NOC"},
		Expires: time.Now().Add(time.Hour).Unix(),
	})

	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.AddCookie(&http.Cookie{Name: a.cfg.CookieName, Value: tok})

	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, r) // note: no middleware, as in production

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: /me must read the cookie itself", rec.Code)
	}
	var got User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Email != "noc@yeti.example" {
		t.Errorf("user = %+v", got)
	}
}

// And with no cookie it answers "nobody" plus where to go, rather than an error.
//
// Table-driven over the mount point, with the expectation derived from it: a
// test that passes "/stats" in and asserts "/stats" out would still pass if the
// code ignored the base and hardcoded it.
func TestMeWithoutASessionOffersTheLoginURL(t *testing.T) {
	for _, base := range []string{"", "/stats", "/yeti/reports"} {
		a := testAuth(t, base)
		rec := httptest.NewRecorder()
		a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("base %q: status = %d, want 401", base, rec.Code)
		}

		var got struct {
			LoginURL string `json:"login_url"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if want := base + "/api/auth/login"; got.LoginURL != want {
			t.Errorf("base %q: login_url = %q, want %q", base, got.LoginURL, want)
		}
	}
}

// --- logout -----------------------------------------------------------------

func logoutReq(t *testing.T, a *Auth, withSession bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	if withSession {
		tok, _ := a.encode(session{
			User:    User{Subject: "u1"},
			Expires: time.Now().Add(time.Hour).Unix(),
			IDToken: "the.id.token",
		})
		r.AddCookie(&http.Cookie{Name: a.cfg.CookieName, Value: tok})
	}
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, r)
	return rec
}

// The default. Signing out of the log viewer must not sign the operator out of
// everything else sharing the IdP — so the local session is
// dropped and the IdP session is left alone.
func TestLocalLogoutDoesNotTouchTheIdP(t *testing.T) {
	a := testAuth(t, "")
	a.logoutURL = "https://idp.example/logout" // the provider offers one...

	rec := logoutReq(t, a, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// ...and we deliberately do not use it.
	if strings.Contains(rec.Body.String(), "provider_logout_url") {
		t.Errorf("local logout must not send the user to the IdP, got %s", rec.Body)
	}
	// The session cookie is still cleared.
	if !clearsSession(t, a, rec) {
		t.Error("logout must clear the session cookie")
	}
}

// Opt-in. RP-initiated logout ends the IdP session too, which signs the user out
// of every app on that issuer.
func TestProviderLogoutEndsTheIdPSession(t *testing.T) {
	a := testAuth(t, "")
	a.cfg.Logout = LogoutProvider
	a.cfg.PostLogoutRedirectURL = "https://logs.example/stats/"
	a.logoutURL = "https://idp.example/logout"

	rec := logoutReq(t, a, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var out struct {
		URL string `json:"provider_logout_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()

	// id_token_hint is what tells the provider whose session to end. Most
	// providers require it, and without it end_session_endpoint is an
	// unauthenticated logout for anyone with the link.
	if q.Get("id_token_hint") != "the.id.token" {
		t.Errorf("id_token_hint = %q, want the session's ID token", q.Get("id_token_hint"))
	}
	if q.Get("post_logout_redirect_uri") != "https://logs.example/stats/" {
		t.Errorf("post_logout_redirect_uri = %q", q.Get("post_logout_redirect_uri"))
	}
	if q.Get("client_id") != a.cfg.ClientID {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if !clearsSession(t, a, rec) {
		t.Error("logout must clear the local session cookie too")
	}
}

// The ID token is a ~1KB JWT. It is only worth carrying in the cookie when
// logout actually needs it as id_token_hint.
func TestIDTokenIsOnlyStoredWhenProviderLogoutNeedsIt(t *testing.T) {
	local := testAuth(t, "")
	if local.cfg.Logout != LogoutLocal {
		t.Fatalf("default logout = %q, want local", local.cfg.Logout)
	}

	prov := testAuth(t, "")
	prov.cfg.Logout = LogoutProvider

	// Round-trip a session as the callback would build it, in each mode.
	for _, tc := range []struct {
		name string
		a    *Auth
		want string
	}{
		{"local", local, ""},
		{"provider", prov, "raw.jwt"},
	} {
		s := session{User: User{Subject: "u"}, Expires: time.Now().Add(time.Hour).Unix()}
		if tc.a.cfg.Logout == LogoutProvider {
			s.IDToken = "raw.jwt"
		}
		tok, _ := tc.a.encode(s)
		got, err := tc.a.decode(tok)
		if err != nil {
			t.Fatal(err)
		}
		if got.IDToken != tc.want {
			t.Errorf("%s: IDToken = %q, want %q", tc.name, got.IDToken, tc.want)
		}
	}
}

func TestUnknownLogoutModeIsRejected(t *testing.T) {
	c := Config{
		Issuer: "https://idp.example", ClientID: "x",
		RedirectURL: "https://s/cb", CookieSecret: secret,
		Logout: "everywhere",
	}
	c.applyDefaults()
	if err := c.validate(); err == nil {
		t.Fatal("an unknown logout mode must be refused, not silently treated as local")
	}
}

// clearsSession reports whether the response expires the session cookie.
func clearsSession(t *testing.T, a *Auth, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == a.cfg.CookieName && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

// --- authorisation ----------------------------------------------------------

func TestAllowedGroupsGateAccess(t *testing.T) {
	a := testAuth(t, "")
	a.cfg.AllowedGroups = []string{"noc", "billing"}

	if err := a.authorize(User{Email: "a@x", Groups: []string{"billing"}}); err != nil {
		t.Errorf("a member of a permitted group should be allowed: %v", err)
	}
	if err := a.authorize(User{Email: "b@x", Groups: []string{"interns"}}); err == nil {
		t.Error("a user in no permitted group must be refused")
	}
	if err := a.authorize(User{Email: "c@x"}); err == nil {
		t.Error("a user with no groups at all must be refused when groups are required")
	}
}

// Without allowed_groups, anyone the IdP knows gets in. That is a real choice,
// so make it an explicit, tested one rather than an accident.
func TestNoAllowedGroupsMeansAnyAuthenticatedUser(t *testing.T) {
	a := testAuth(t, "")
	if err := a.authorize(User{Email: "anyone@example.com"}); err != nil {
		t.Errorf("with no allowed_groups, any authenticated user is permitted: %v", err)
	}
}

// --- open redirect ----------------------------------------------------------

// return_to is attacker-controllable, so it must not be able to bounce the user
// off-site wearing our domain.
func TestReturnToCannotLeaveTheApp(t *testing.T) {
	for _, evil := range []string{
		"https://evil.example/phish",
		"//evil.example/phish",
		"javascript:alert(1)",
		"",
	} {
		if got := safeReturn(evil); got != "/" {
			t.Errorf("safeReturn(%q) = %q; must not leave the app", evil, got)
		}
	}
	if got := safeReturn("/traffic/vendor-traffic"); got != "/traffic/vendor-traffic" {
		t.Errorf("an in-app path should survive, got %q", got)
	}
}

// --- config -----------------------------------------------------------------

func TestConfigRejectsAWeakCookieSecret(t *testing.T) {
	c := Config{
		Issuer: "https://idp.example", ClientID: "x",
		RedirectURL: "https://s/cb", CookieSecret: "short",
	}
	c.applyDefaults()
	if err := c.validate(); err == nil {
		t.Fatal("a cookie_secret under 32 bytes must be refused: it looks like security without being it")
	}
}

// Sharing a domain with another app at /, our cookie must be scoped to our
// mount so that app never receives it. Derived from the base, not hardcoded.
func TestCookieIsScopedToTheMountPoint(t *testing.T) {
	cases := map[string]string{
		"":              "/",
		"/stats":        "/stats/",
		"/yeti/reports": "/yeti/reports/",
	}
	for base, want := range cases {
		if got := testAuth(t, base).cookiePath(); got != want {
			t.Errorf("base %q: cookie path = %q, want %q", base, got, want)
		}
	}
}

// The redirect after login must land inside whatever sub-directory the app is
// mounted at, not at the domain root.
func TestPostLoginRedirectStaysUnderTheMount(t *testing.T) {
	for _, base := range []string{"", "/stats", "/yeti/reports"} {
		a := testAuth(t, base)
		// This is the concatenation the callback performs.
		if got := a.base + safeReturn("/traffic/vendor-traffic"); got != base+"/traffic/vendor-traffic" {
			t.Errorf("base %q: redirect = %q", base, got)
		}
	}
}

func TestSessionCookieIsHttpOnly(t *testing.T) {
	a := testAuth(t, "")
	rec := httptest.NewRecorder()
	a.setCookie(rec, a.cfg.CookieName, "v", time.Hour)

	c := rec.Result().Cookies()[0]
	if !c.HttpOnly {
		t.Error("the session cookie must be HttpOnly: an XSS anywhere in the SPA would otherwise steal it")
	}
	if !c.Secure {
		t.Error("the session cookie must be Secure by default")
	}
	// Lax, not Strict: the IdP redirects back cross-site, and Strict would
	// withhold the cookie on that navigation and loop the login forever.
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
}

var _ = context.Background

// Validate is what `vlui -check-config` calls. It must catch what New would
// reject, without needing a provider to talk to — the whole point is to be
// usable before a restart and in CI.
func TestValidateCatchesWhatWouldFailAtStartup(t *testing.T) {
	good := Config{
		Enabled:      true,
		Issuer:       "https://idp.example",
		ClientID:     "vlui",
		RedirectURL:  "https://logs.example/api/auth/callback",
		CookieSecret: secret,
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("a usable config was rejected: %v", err)
	}

	cases := map[string]func(*Config){
		"no issuer":       func(c *Config) { c.Issuer = "" },
		"no client id":    func(c *Config) { c.ClientID = "" },
		"no redirect url": func(c *Config) { c.RedirectURL = "" },
		"short secret":    func(c *Config) { c.CookieSecret = "too short" },
		"unknown logout":  func(c *Config) { c.Logout = "sometimes" },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := good
			break_(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("want an error")
			}
		})
	}

	// Validating must not mutate the caller's config: it fills in defaults on a
	// copy, and a Validate that wrote them back would make a check-then-use
	// caller behave differently from one that skipped the check.
	//
	// Compared field by field rather than with ==: Config holds a []string, so
	// it is not comparable.
	before := fmt.Sprintf("%+v", good)
	_ = good.Validate()
	if after := fmt.Sprintf("%+v", good); after != before {
		t.Errorf("Validate mutated its receiver:\n  before %s\n  after  %s", before, after)
	}
}
