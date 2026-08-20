package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/oauth2"
)

type Auth struct {
	cfg  Config
	base string
	log  *slog.Logger

	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	// logoutURL is the provider's RP-initiated logout endpoint, when it has one.
	logoutURL string
}

type ctxKey struct{}

// New sets up OIDC. It talks to the issuer, so it can fail when the IdP is
// unreachable — which is deliberate: starting with authentication silently
// broken would serve every log line to anyone.
func New(ctx context.Context, cfg Config, base string, log *slog.Logger) (*Auth, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	// A generated secret is a working session cookie and a broken deployment,
	// so it says so once, loudly, rather than being discovered when people
	// start reporting random logouts.
	if cfg.CookieSecret == "" {
		cfg.CookieSecret = randString()
		log.Warn("auth: cookie_secret is not set, so a random one was generated for this process",
			"consequence", "every restart signs everyone out, and a second instance will reject this one's sessions",
			"fix", "set auth.cookie_secret to a value of at least 32 bytes: openssl rand -hex 32")
	}

	a := &Auth{cfg: cfg, base: base, log: log}

	var endpoint oauth2.Endpoint

	if cfg.AuthURL != "" {
		// Endpoints given by hand: for a provider with no discovery document.
		endpoint = oauth2.Endpoint{AuthURL: cfg.AuthURL, TokenURL: cfg.TokenURL}
		if cfg.JWKSURL == "" {
			return nil, errors.New("auth: jwks_url is required when endpoints are configured by hand")
		}
		ks := oidc.NewRemoteKeySet(ctx, cfg.JWKSURL)
		a.verifier = oidc.NewVerifier(cfg.Issuer, ks, &oidc.Config{ClientID: cfg.ClientID})
	} else {
		// The normal path: everything from /.well-known/openid-configuration.
		provider, err := oidc.NewProvider(ctx, cfg.Issuer)
		if err != nil {
			return nil, fmt.Errorf("auth: OIDC discovery on %s: %w", cfg.Issuer, err)
		}
		endpoint = provider.Endpoint()
		a.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

		// end_session_endpoint is optional, and not part of the core struct.
		var extra struct {
			EndSession string `json:"end_session_endpoint"`
		}
		if err := provider.Claims(&extra); err == nil {
			a.logoutURL = extra.EndSession
		}
	}

	a.oauth = &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     endpoint,
		Scopes:       cfg.Scopes,
	}

	log.Info("OIDC configured", "issuer", cfg.Issuer, "client_id", cfg.ClientID)
	return a, nil
}

// Routes are the auth endpoints. They are mounted inside /api but must stay
// outside the middleware, or logging in would require being logged in.
func (a *Auth) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/login", a.login)
	r.Get("/callback", a.callback)
	r.Post("/logout", a.logout)
	r.Get("/me", a.me)
	return r
}

const stateCookie = "vlui_oidc"

// flow is the short-lived state carried across the redirect to the IdP.
type flow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`  // PKCE
	Return   string `json:"rt"` // where the user was going
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	f := flow{
		State:    randString(),
		Nonce:    randString(),
		Verifier: oauth2.GenerateVerifier(),
		Return:   a.safeReturn(r.URL.Query().Get("return_to")),
	}

	// The flow lives in a signed cookie rather than in server memory, so the
	// login survives a restart and works across replicas with no shared store.
	payload, err := json.Marshal(f)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	a.setCookie(w, stateCookie, body+"."+a.sign(body), 10*time.Minute)

	// PKCE even though this is a confidential client: it costs nothing and it
	// closes code interception if the redirect ever leaks.
	url := a.oauth.AuthCodeURL(f.State,
		oidc.Nonce(f.Nonce),
		oauth2.S256ChallengeOption(f.Verifier),
	)
	http.Redirect(w, r, url, http.StatusFound)
}

func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	if e := r.URL.Query().Get("error"); e != "" {
		a.fail(w, r, http.StatusForbidden,
			fmt.Errorf("provider refused: %s: %s", e, r.URL.Query().Get("error_description")))
		return
	}

	c, err := r.Cookie(stateCookie)
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, errors.New("no login in progress (cookie missing or expired)"))
		return
	}
	a.clearCookie(w, stateCookie)

	f, err := a.decodeFlow(c.Value)
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, err)
		return
	}
	// CSRF: the state we get back must be the state we sent.
	if r.URL.Query().Get("state") != f.State {
		a.fail(w, r, http.StatusBadRequest, errors.New("state mismatch"))
		return
	}

	token, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"),
		oauth2.VerifierOption(f.Verifier))
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, fmt.Errorf("token exchange: %w", err))
		return
	}

	raw, ok := token.Extra("id_token").(string)
	if !ok {
		// An OAuth2-only provider lands here, as does a conformant one whose
		// registered client is missing the `openid` scope. Say so plainly
		// rather than failing as a generic auth error.
		a.fail(w, r, http.StatusInternalServerError,
			errors.New("provider returned no id_token: it is OAuth2, not OIDC"))
		return
	}

	idToken, err := a.verifier.Verify(r.Context(), raw)
	if err != nil {
		a.fail(w, r, http.StatusForbidden, fmt.Errorf("id_token: %w", err))
		return
	}
	// Replay: the nonce binds this token to the login we started.
	if idToken.Nonce != f.Nonce {
		a.fail(w, r, http.StatusForbidden, errors.New("nonce mismatch"))
		return
	}

	user, err := a.userFrom(idToken)
	if err != nil {
		a.fail(w, r, http.StatusForbidden, err)
		return
	}
	if err := a.authorize(user); err != nil {
		a.fail(w, r, http.StatusForbidden, err)
		return
	}

	s := session{
		User:    user,
		Expires: time.Now().Add(a.cfg.SessionTTL).Unix(),
	}
	// Only carried when logout actually needs it as id_token_hint — see session.
	if a.cfg.Logout == LogoutProvider {
		s.IDToken = raw
	}

	tok, err := a.encode(s)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	a.setCookie(w, a.cfg.CookieName, tok, a.cfg.SessionTTL)

	a.log.Info("login", "sub", user.Subject, "email", user.Email)
	http.Redirect(w, r, a.base+f.Return, http.StatusFound)
}

func (a *Auth) userFrom(t *oidc.IDToken) (User, error) {
	var claims map[string]any
	if err := t.Claims(&claims); err != nil {
		return User{}, fmt.Errorf("id_token claims: %w", err)
	}

	u := User{
		Subject: t.Subject,
		Email:   str(claims["email"]),
		Name:    str(claims["name"]),
	}
	if u.Name == "" {
		u.Name = str(claims["preferred_username"])
	}
	if u.Name == "" {
		u.Name = u.Email
	}
	u.Groups = claimValues(claims, a.cfg.GroupsClaim)

	// Which claims arrived, and what came out of the configured one. This is
	// the log line that answers "why am I in none of the permitted groups"
	// without a rebuild — the shape and the name of the group claim differ per
	// provider, and the token is the only place that says which you have.
	a.log.Debug("id_token claims",
		"sub", t.Subject,
		"claims", slices.Sorted(maps.Keys(claims)),
		"groups_claim", a.cfg.GroupsClaim,
		"groups", u.Groups,
	)

	return u, nil
}

// authorize is the difference between "the IdP knows you" and "you may read
// the logs". Without allowed_groups, any account in the directory qualifies.
func (a *Auth) authorize(u User) error {
	if len(a.cfg.AllowedGroups) == 0 {
		return nil
	}
	for _, g := range u.Groups {
		if slices.Contains(a.cfg.AllowedGroups, g) {
			return nil
		}
	}
	// Names the claim it read and what it found there. The usual cause is not
	// that the account is in the wrong group but that the groups never arrived:
	// a provider that puts them somewhere else, or a token that carries none.
	who := u.Email
	if who == "" {
		who = u.Subject
	}
	found := "no groups at all"
	if len(u.Groups) > 0 {
		found = fmt.Sprintf("groups %v", u.Groups)
	}
	return fmt.Errorf("account %q has %s from claim %q, and none of them are in allowed_groups %v",
		who, found, a.cfg.GroupsClaim, a.cfg.AllowedGroups)
}

// logout always drops our session. Whether it also ends the IdP session is a
// deployment decision, not ours to make:
//
//   - local (the default): the IdP session survives, so signing back in is one
//     click with no prompt. That is the SSO contract, and it leaves every other
//     app on the issuer alone.
//
//   - provider: RP-initiated logout, ending the IdP session too — and therefore
//     signing the user out of every app on that IdP. A large blast radius for a
//     button in a log viewer, so it is opt-in.
func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	s, hadSession := a.sessionFull(r)
	a.clearCookie(w, a.cfg.CookieName)

	out := map[string]string{}

	if a.cfg.Logout == LogoutProvider && a.logoutURL != "" && hadSession {
		u, err := url.Parse(a.logoutURL)
		if err == nil {
			q := u.Query()
			// id_token_hint tells the provider whose session to end. Most
			// providers require it, and it is what stops end_session_endpoint
			// being an unauthenticated logout-CSRF for anyone with the link.
			if s.IDToken != "" {
				q.Set("id_token_hint", s.IDToken)
			}
			q.Set("client_id", a.cfg.ClientID)
			if a.cfg.PostLogoutRedirectURL != "" {
				q.Set("post_logout_redirect_uri", a.cfg.PostLogoutRedirectURL)
			}
			u.RawQuery = q.Encode()
			out["provider_logout_url"] = u.String()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// me answers "who am I", including "nobody".
//
// It reads the cookie itself rather than the request context: /auth/* is mounted
// outside the auth middleware — it must be, or logging in would require being
// logged in — so nothing has populated the context here. Taking the user from
// the context would make this endpoint answer 401 even for a perfectly valid
// session, which is exactly what it did until it was tested end to end.
func (a *Auth) me(w http.ResponseWriter, r *http.Request) {
	u, ok := a.session(r)
	if !ok {
		a.unauthorized(w)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// session reads and verifies the session cookie, if there is one.
func (a *Auth) session(r *http.Request) (User, bool) {
	s, ok := a.sessionFull(r)
	return s.User, ok
}

// sessionFull also yields the ID token, which only logout needs.
func (a *Auth) sessionFull(r *http.Request) (session, bool) {
	c, err := r.Cookie(a.cfg.CookieName)
	if err != nil {
		return session{}, false
	}
	s, err := a.decode(c.Value)
	if err != nil {
		return session{}, false
	}
	return s, true
}

// Middleware attaches the session to the request, and rejects requests without
// one. The SPA shell itself is served unauthenticated — it is only JavaScript,
// and it asks /api/auth/me who it is talking to before it shows anything.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.session(r)
		if !ok {
			// An expired or tampered cookie is cleared, so the browser stops
			// resending it on every request.
			if _, err := r.Cookie(a.cfg.CookieName); err == nil {
				a.clearCookie(w, a.cfg.CookieName)
			}
			a.unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

func (a *Auth) unauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{
		"error":     "not authenticated",
		"login_url": a.base + "/api/auth/login",
	})
}

// WithUser / UserFrom carry the authenticated user on the request context.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

// --- helpers ----------------------------------------------------------------

func (a *Auth) decodeFlow(token string) (flow, error) {
	var f flow
	body, sig, ok := strings.Cut(token, ".")
	if !ok || sig != a.sign(body) {
		return f, errors.New("bad login state")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return f, errors.New("bad login state")
	}
	return f, json.Unmarshal(payload, &f)
}

// safeReturn keeps the post-login redirect inside this app. An open redirect
// here would let a crafted login link bounce the user to an attacker's page
// wearing our domain.
//
// The path it returns is relative to base_path, which the caller prepends. A
// return_to that ALREADY carries the base — an older client, or somebody
// building the URL by hand from what the address bar showed — would otherwise
// come back to /logs/logs/, so the duplicate prefix is stripped here rather
// than being anyone else's problem.
func (a *Auth) safeReturn(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	if a.base != "" {
		if p == a.base {
			return "/"
		}
		if after, found := strings.CutPrefix(p, a.base+"/"); found {
			return "/" + after
		}
	}
	return p
}

func randString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is not a condition we can carry on through
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// claimValues reads group or role names out of an id_token claim named by
// auth.groups_claim.
//
// Two things vary between providers, and both have to be configuration rather
// than assumptions, or vlui only works with the provider it was written
// against:
//
//   - WHERE the names live. A flat "groups" for most; Zitadel puts roles under
//     "urn:zitadel:iam:org:project:roles"; Keycloak nests client roles at
//     "resource_access.<client>.roles". A dotted path walks nested objects, and
//     an exact key is tried FIRST so a claim whose own name contains a dot
//     still resolves.
//
//   - WHAT SHAPE they are in. An array of strings for most; Zitadel uses an
//     OBJECT whose KEYS are the role names and whose values describe which
//     organisation granted them; a lone string for a provider with one group.
//     Reading only arrays is what made a Zitadel token look like an account in
//     no groups at all.
func claimValues(claims map[string]any, path string) []string {
	if path == "" {
		return nil
	}
	if v, ok := claims[path]; ok {
		return asStrings(v)
	}

	var cur any = claims
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[part]; !ok {
			return nil
		}
	}
	return asStrings(cur)
}

func asStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out

	case map[string]any:
		// Zitadel's shape: the keys are the role names. Sorted, so a log line
		// and an error message read the same way twice running.
		return slices.Sorted(maps.Keys(t))

	case string:
		// One group, not a list to be split: a name may legitimately contain a
		// space or a comma, and guessing a separator would corrupt it.
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}

// fail writes an error response. As in the api package, a 5xx withholds its
// cause: a failed token exchange returns whatever the IdP said, which can quote
// our own request — client id and all — back at us. The 4xx messages here
// ("state mismatch", "nonce mismatch", "provider refused: …") are ours and are
// about this login attempt, so a user who cannot sign in still learns which step
// broke.
func (a *Auth) fail(w http.ResponseWriter, r *http.Request, code int, err error) {
	id := middleware.GetReqID(r.Context())
	a.log.Warn("auth failed", "path", r.URL.Path, "code", code, "request_id", id, "err", err)

	if code >= 500 {
		writeJSON(w, code, map[string]string{"error": "internal error", "request_id": id})
		return
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"encode"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}
