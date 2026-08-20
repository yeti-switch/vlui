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
		if got := (&Auth{}).safeReturn(evil); got != "/" {
			t.Errorf("safeReturn(%q) = %q; must not leave the app", evil, got)
		}
	}
	if got := (&Auth{}).safeReturn("/traffic/vendor-traffic"); got != "/traffic/vendor-traffic" {
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
		if got := a.base + a.safeReturn("/traffic/vendor-traffic"); got != base+"/traffic/vendor-traffic" {
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

// Where the group names live and what shape they are in both vary by provider.
// Reading only a flat array of strings is what made a Zitadel token — whose
// roles are an OBJECT under a URN — look like an account in no groups at all.
func TestClaimValuesAcrossProviders(t *testing.T) {
	cases := []struct {
		name   string
		claim  string
		claims map[string]any
		want   []string
	}{
		{
			name:  "flat array, the common case",
			claim: "groups",
			claims: map[string]any{
				"groups": []any{"noc", "sre"},
			},
			want: []string{"noc", "sre"},
		},
		{
			// The keys are the role names; the values say which organisation
			// granted them and are not what we match on.
			name:  "zitadel: object under a URN",
			claim: "urn:zitadel:iam:org:project:roles",
			claims: map[string]any{
				"urn:zitadel:iam:org:project:roles": map[string]any{
					"noc":   map[string]any{"178": "acme.zitadel.cloud"},
					"admin": map[string]any{"178": "acme.zitadel.cloud"},
				},
			},
			want: []string{"admin", "noc"}, // sorted, so it reads the same twice
		},
		{
			name:  "keycloak: nested client roles",
			claim: "resource_access.vlui.roles",
			claims: map[string]any{
				"resource_access": map[string]any{
					"vlui": map[string]any{"roles": []any{"noc"}},
				},
			},
			want: []string{"noc"},
		},
		{
			// A name may contain a space or a comma, so a lone string is one
			// group rather than a list to be split.
			name:   "single string",
			claim:  "groups",
			claims: map[string]any{"groups": "network operations"},
			want:   []string{"network operations"},
		},
		{
			// The exact key wins over path traversal, or a provider whose claim
			// name contains a dot would be unreachable.
			name:  "literal key containing a dot",
			claim: "acme.io/groups",
			claims: map[string]any{
				"acme.io/groups": []any{"noc"},
				"acme":           map[string]any{"io/groups": []any{"wrong"}},
			},
			want: []string{"noc"},
		},
		{
			name:   "claim absent",
			claim:  "groups",
			claims: map[string]any{"sub": "u-1"},
			want:   nil,
		},
		{
			name:   "path into something that is not an object",
			claim:  "sub.groups",
			claims: map[string]any{"sub": "u-1"},
			want:   nil,
		},
		{
			name:   "numbers and nulls in the array are skipped",
			claim:  "groups",
			claims: map[string]any{"groups": []any{"noc", 42, nil}},
			want:   []string{"noc"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := claimValues(tc.claims, tc.claim)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The refusal has to say which claim it read and what was in it. "You are in
// none of the permitted groups" sends the operator to the wrong place when the
// truth is that no groups arrived at all.
func TestRefusalNamesTheClaimAndWhatItFound(t *testing.T) {
	a := &Auth{cfg: Config{
		AllowedGroups: []string{"noc"},
		GroupsClaim:   "urn:zitadel:iam:org:project:roles",
	}}

	err := a.authorize(User{Subject: "u-1"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, want := range []string{"u-1", "no groups at all", "urn:zitadel:iam:org:project:roles", "noc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}

	// And when groups did arrive, it says which — the difference between a
	// misconfigured claim and a genuine "not your team".
	err = a.authorize(User{Subject: "u-1", Email: "op@example.com", Groups: []string{"billing"}})
	if !strings.Contains(err.Error(), "billing") || !strings.Contains(err.Error(), "op@example.com") {
		t.Errorf("error does not report what was found: %v", err)
	}
}

// The base path is prepended by the caller, so a return_to that already carries
// it must not be prefixed twice — signing in at /logs/ would otherwise land on
// /logs/logs/, which serves the SPA's history fallback and looks like the app
// forgot where it was.
func TestSafeReturnDoesNotDoubleTheBasePath(t *testing.T) {
	a := &Auth{base: "/logs"}

	cases := map[string]string{
		"/logs/":          "/",
		"/logs":           "/",
		"/logs/#q=error":  "/#q=error",
		"/logs/deep/link": "/deep/link",
		"/":               "/",
		"/#q=error":       "/#q=error",
		// Not ours to strip: a path that merely starts with the same letters.
		"/logsomething": "/logsomething",
	}
	for in, want := range cases {
		if got := a.safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", in, got, want)
		}
		// What the caller actually redirects to. Compared against the expected
		// landing rather than sniffed for a "/logs/logs" prefix: that heuristic
		// flags "/logsomething" -> "/logs/logsomething", which is correct.
		if landing, want := a.base+a.safeReturn(in), a.base+want; landing != want {
			t.Errorf("return_to %q lands on %q, want %q", in, landing, want)
		}
	}

	// With no base path there is nothing to strip.
	plain := &Auth{}
	if got := plain.safeReturn("/logs/"); got != "/logs/" {
		t.Errorf("with no base, safeReturn(%q) = %q", "/logs/", got)
	}
}

// fakeProvider is just enough OIDC to get through New: go-oidc fetches the
// discovery document and nothing else until a token is verified, and the
// issuer in it must match the URL it was fetched from.
func fakeProvider(t *testing.T) string {
	t.Helper()

	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	issuer = srv.URL
	t.Cleanup(srv.Close)
	return issuer
}

// An empty cookie_secret is a trial convenience, not a configuration error: the
// process generates one so `auth.enabled: true` works with nothing else set.
func TestGeneratedCookieSecret(t *testing.T) {
	idp := fakeProvider(t)

	cfg := Config{
		Enabled:     true,
		Issuer:      idp,
		ClientID:    "vlui",
		RedirectURL: "https://logs.example/api/auth/callback",
		// No CookieSecret.
	}

	// It must pass the config check, or -check-config would refuse a
	// configuration that starts perfectly well.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an empty cookie_secret must be allowed: %v", err)
	}

	a, err := New(t.Context(), cfg, "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	// And the generated one must actually work: a session it signs verifies.
	tok, err := a.encode(session{User: User{Subject: "u-1"}, Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.decode(tok); err != nil {
		t.Fatalf("a session signed with the generated secret does not verify: %v", err)
	}

	// The caveat, as a test rather than only as a warning in the log: two
	// processes generate two secrets, so one's cookies are refused by the
	// other. This is why it must not be relied on with replicas or across a
	// restart.
	b, err := New(t.Context(), cfg, "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.decode(tok); err == nil {
		t.Error("a second process accepted the first one's session — the secrets are supposed to differ")
	}
}

// Short is still refused. Unlike empty, it is clearly something somebody meant,
// and it looks like security while being guessable.
func TestShortCookieSecretIsStillRefused(t *testing.T) {
	cfg := Config{
		Enabled:      true,
		Issuer:       "https://idp.example",
		ClientID:     "vlui",
		RedirectURL:  "https://logs.example/api/auth/callback",
		CookieSecret: "too short",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("want an error")
	}
	// And it should point at the alternative rather than only complaining.
	if !strings.Contains(err.Error(), "leave it empty") {
		t.Errorf("error does not mention the generated option: %v", err)
	}
}
