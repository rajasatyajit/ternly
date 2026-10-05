// Package mcpauth is the OAuth 2.1 client for remote MCP servers, as the
// MCP authorization spec (2026-07-28) requires: protected-resource and
// authorization-server metadata discovery in the spec's order, exact issuer
// checks, PKCE S256 (refused without it), the resource indicator on every
// authorization and token request, Client ID Metadata Documents or Dynamic
// Client Registration, a loopback redirect, the RFC 9207 iss check, refresh,
// and scope step-up (ADR 014).
package mcpauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rajasatyajit/ternly/internal/mcphttp"
)

// Flow authorizes ternly to one MCP server.
type Flow struct {
	Resource string       // the server's canonical URL (the RFC 8707 resource)
	HTTP     *http.Client // the server's network grant
	Store    Store
	// Approve is asked before any host other than the server's own is
	// contacted (its authorization server, token or registration endpoint).
	// It extends the grant, or returns an error to refuse.
	Approve func(host string) error
	// Open shows the authorization URL to the user (a browser); Notify tells
	// them what is happening.
	Open   func(url string) error
	Notify func(msg string)
	// Client is a pre-registered client, if the user configured one.
	Client *Registered
	// MetadataURL is this client's Client ID Metadata Document URL, if one is
	// published (preferred over Dynamic Client Registration when supported).
	MetadataURL string
	ClientName  string
	Timeout     time.Duration // to complete the browser login (default 5 min)
	// Interactive allows a browser login; otherwise a challenge that a
	// refresh can't answer fails with ErrLoginRequired (start-up and tool
	// calls never open a browser — only /mcp login does).
	Interactive bool

	mu sync.Mutex
}

// Registered is a client registration at one authorization server.
type Registered struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string // the loopback URI it was registered with
}

type prm struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type asMeta struct {
	Issuer                   string   `json:"issuer"`
	AuthorizationEndpoint    string   `json:"authorization_endpoint"`
	TokenEndpoint            string   `json:"token_endpoint"`
	RegistrationEndpoint     string   `json:"registration_endpoint"`
	CodeChallengeMethods     []string `json:"code_challenge_methods_supported"`
	CIMDSupported            bool     `json:"client_id_metadata_document_supported"`
	ISSParameterSupported    bool     `json:"authorization_response_iss_parameter_supported"`
	ScopesSupported          []string `json:"scopes_supported"`
	TokenEndpointAuthMethods []string `json:"token_endpoint_auth_methods_supported"`
}

// ErrLoginRequired: the server needs a login, which only an explicit
// /mcp login (an interactive Flow) does.
var ErrLoginRequired = errors.New("login required")

// Token returns a usable access token for the server: the stored one,
// refreshed first when it has expired; "" when there is none (the server
// will challenge, and Authorize runs).
func (f *Flow) Token(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok, err := f.find()
	if err != nil || !ok || c.AccessToken == "" {
		return "", err
	}
	if !c.Expiry.IsZero() && time.Until(c.Expiry) < 30*time.Second {
		if c.RefreshToken == "" {
			return "", nil
		}
		meta, err := f.authServer(ctx, c.Issuer)
		if err != nil {
			return "", err
		}
		if err := f.refresh(ctx, meta, &c); err != nil {
			return "", nil // let the server challenge; Authorize logs in again
		}
	}
	return c.AccessToken, nil
}

// find returns the credential stored for this resource, whichever issuer.
func (f *Flow) find() (Credential, bool, error) {
	if l, ok := f.Store.(interface {
		ByResource(string) (Credential, bool, error)
	}); ok {
		return l.ByResource(f.Resource)
	}
	return Credential{}, false, nil
}

// Authorize answers a challenge: discovery, registration, login (or
// refresh), token. On return the stored token satisfies the challenge.
func (f *Flow) Authorize(ctx context.Context, ch mcphttp.Challenge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, err := f.protectedResource(ctx, ch.ResourceMetadata)
	if err != nil {
		return err
	}
	if len(pr.AuthorizationServers) == 0 {
		return errors.New("the server's resource metadata names no authorization server")
	}
	meta, err := f.authServer(ctx, pr.AuthorizationServers[0])
	if err != nil {
		return err
	}
	prev, have, err := f.Store.Get(meta.Issuer, f.Resource)
	if err != nil {
		return err
	}
	// Scopes: the challenge's are authoritative; else all the resource supports.
	scopes := strings.Fields(strings.ReplaceAll(ch.Scope, ",", " "))
	if len(scopes) == 0 {
		scopes = pr.ScopesSupported
	}
	stepUp := ch.Error == "insufficient_scope"
	if stepUp {
		scopes = union(prev.Scopes, scopes)
	}
	if have && prev.RefreshToken != "" && !stepUp {
		if err := f.refresh(ctx, meta, &prev); err == nil {
			return nil
		}
	}
	if !f.Interactive {
		return ErrLoginRequired
	}
	reg, err := f.registration(ctx, meta, prev, have)
	if err != nil {
		return err
	}
	c, err := f.login(ctx, meta, reg, scopes)
	if err != nil {
		return err
	}
	return f.Store.Put(c)
}

// protectedResource fetches RFC 9728 metadata: from the challenge's URL,
// else the two well-known URLs in the spec's order.
func (f *Flow) protectedResource(ctx context.Context, fromChallenge string) (prm, error) {
	u, err := url.Parse(f.Resource)
	if err != nil {
		return prm{}, err
	}
	var tries []string
	if fromChallenge != "" {
		tries = []string{fromChallenge}
	} else {
		base := u.Scheme + "://" + u.Host
		if p := strings.Trim(u.Path, "/"); p != "" {
			tries = append(tries, base+"/.well-known/oauth-protected-resource/"+p)
		}
		tries = append(tries, base+"/.well-known/oauth-protected-resource")
	}
	var last error
	for _, t := range tries {
		var p prm
		if last = f.getJSON(ctx, t, &p); last == nil {
			if p.Resource != "" && strings.TrimSuffix(p.Resource, "/") != strings.TrimSuffix(f.Resource, "/") {
				return prm{}, fmt.Errorf("resource metadata is for %q, not %q", p.Resource, f.Resource)
			}
			return p, nil
		}
	}
	return prm{}, fmt.Errorf("protected resource metadata: %w", last)
}

// authServer fetches authorization-server metadata from the well-known URLs
// in the spec's order, and uses it only if its issuer is exactly the one
// asked for and it supports PKCE S256.
func (f *Flow) authServer(ctx context.Context, issuer string) (asMeta, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return asMeta{}, fmt.Errorf("invalid issuer %q", issuer)
	}
	if err := f.allow(u.Host); err != nil {
		return asMeta{}, err
	}
	base, p := u.Scheme+"://"+u.Host, strings.Trim(u.Path, "/")
	tries := []string{base + "/.well-known/oauth-authorization-server", base + "/.well-known/openid-configuration"}
	if p != "" {
		tries = []string{base + "/.well-known/oauth-authorization-server/" + p, base + "/.well-known/openid-configuration/" + p, base + "/" + p + "/.well-known/openid-configuration"}
	}
	var last error
	for _, t := range tries {
		var m asMeta
		if last = f.getJSON(ctx, t, &m); last != nil {
			continue
		}
		if m.Issuer != issuer {
			return asMeta{}, fmt.Errorf("authorization server metadata at %s names issuer %q, not %q: not used", t, m.Issuer, issuer)
		}
		if !slices.Contains(m.CodeChallengeMethods, "S256") {
			return asMeta{}, fmt.Errorf("the authorization server %s doesn't advertise PKCE S256: refusing to log in", issuer)
		}
		if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
			return asMeta{}, fmt.Errorf("authorization server metadata for %s lacks its endpoints", issuer)
		}
		for _, e := range []string{m.AuthorizationEndpoint, m.TokenEndpoint, m.RegistrationEndpoint} {
			if e == "" {
				continue
			}
			// The authorization endpoint goes to the user's browser: only
			// https (http on loopback), never another scheme or an argument.
			if err := WebURL(e); err != nil {
				return asMeta{}, fmt.Errorf("authorization server %s: %w", issuer, err)
			}
			if eu, _ := url.Parse(e); eu.Host != u.Host {
				if err := f.allow(eu.Host); err != nil {
					return asMeta{}, err
				}
			}
		}
		return m, nil
	}
	return asMeta{}, fmt.Errorf("authorization server metadata for %s: %w", issuer, last)
}

// WebURL accepts an absolute https URL, or http on a loopback host.
func WebURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || strings.HasPrefix(s, "-") {
		return fmt.Errorf("invalid endpoint %q", s)
	}
	switch h := u.Hostname(); {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && (h == "localhost" || net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()):
		return nil
	}
	return fmt.Errorf("endpoint %q is not https", s)
}

func (f *Flow) allow(host string) error {
	if r, err := url.Parse(f.Resource); err == nil && strings.EqualFold(r.Host, host) {
		return nil
	}
	if f.Approve == nil {
		return fmt.Errorf("%s is not granted to this server", host)
	}
	return f.Approve(host)
}

// registration returns the client to log in as: pre-registered, stored for
// this issuer, a metadata document, or a new dynamic registration.
func (f *Flow) registration(ctx context.Context, meta asMeta, prev Credential, have bool) (Registered, error) {
	switch {
	case f.Client != nil:
		return *f.Client, nil
	case have && prev.ClientID != "":
		return Registered{ClientID: prev.ClientID, ClientSecret: prev.ClientSecret, RedirectURI: prev.RedirectURI}, nil
	case f.MetadataURL != "" && meta.CIMDSupported:
		port, err := freePort()
		if err != nil {
			return Registered{}, err
		}
		return Registered{ClientID: f.MetadataURL, RedirectURI: fmt.Sprintf("http://127.0.0.1:%d/callback", port)}, nil
	case meta.RegistrationEndpoint != "":
		port, err := freePort()
		if err != nil {
			return Registered{}, err
		}
		redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
		body, _ := json.Marshal(map[string]any{
			"client_name": orStr(f.ClientName, "ternly"), "redirect_uris": []string{redirect}, "application_type": "native",
			"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
			"token_endpoint_auth_method": "none",
		})
		var out struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		}
		if err := f.postJSON(ctx, meta.RegistrationEndpoint, body, &out); err != nil {
			return Registered{}, fmt.Errorf("dynamic client registration: %w", err)
		}
		if out.ClientID == "" {
			return Registered{}, errors.New("dynamic client registration returned no client_id")
		}
		return Registered{ClientID: out.ClientID, ClientSecret: out.ClientSecret, RedirectURI: redirect}, nil
	}
	return Registered{}, fmt.Errorf("the authorization server %s offers no way for ternly to register (no metadata document support, no registration endpoint): configure a client_id for this server", meta.Issuer)
}

// login runs the authorization-code flow with PKCE through the browser.
func (f *Flow) login(ctx context.Context, meta asMeta, reg Registered, scopes []string) (Credential, error) {
	ru, err := url.Parse(reg.RedirectURI)
	if err != nil {
		return Credential{}, err
	}
	ln, err := net.Listen("tcp", ru.Host) // the registered loopback port
	if err != nil {
		return Credential{}, fmt.Errorf("the redirect address %s is busy (%v): log in again to register a new one", ru.Host, err)
	}
	defer ln.Close()
	verifier, state := random(32), random(16)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {reg.RedirectURI},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {state}, "resource": {f.Resource}}
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	authURL := meta.AuthorizationEndpoint + sep(meta.AuthorizationEndpoint) + q.Encode()
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ru.Path {
			http.NotFound(w, r)
			return
		}
		v := r.URL.Query()
		res := result{code: v.Get("code")}
		switch iss := v.Get("iss"); {
		case v.Get("state") != state:
			res = result{err: errors.New("the login response's state doesn't match: ignored")}
		case iss != "" && iss != meta.Issuer, iss == "" && meta.ISSParameterSupported:
			res = result{err: fmt.Errorf("the login response came from issuer %q, not %q: refused (RFC 9207)", iss, meta.Issuer)}
		case v.Get("error") != "":
			res = result{err: fmt.Errorf("login refused: %s %s", v.Get("error"), v.Get("error_description"))}
		case res.code == "":
			res = result{err: errors.New("the login response has no code")}
		}
		if res.err != nil {
			http.Error(w, "ternly: "+res.err.Error(), 400)
		} else {
			fmt.Fprint(w, "ternly is authorized. You can close this tab.")
		}
		select {
		case done <- res:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	if f.Notify != nil {
		f.Notify("log in to authorize ternly: " + authURL)
	}
	if f.Open != nil {
		_ = f.Open(authURL)
	}
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	var res result
	select {
	case res = <-done:
	case <-time.After(timeout):
		return Credential{}, errors.New("timed out waiting for the browser login")
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	}
	if res.err != nil {
		return Credential{}, res.err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {res.code}, "redirect_uri": {reg.RedirectURI},
		"client_id": {reg.ClientID}, "code_verifier": {verifier}, "resource": {f.Resource}}
	c := Credential{Issuer: meta.Issuer, Resource: f.Resource, ClientID: reg.ClientID, ClientSecret: reg.ClientSecret, RedirectURI: reg.RedirectURI}
	if err := f.token(ctx, meta, form, reg.ClientSecret, &c); err != nil {
		return Credential{}, err
	}
	if c.Scopes == nil {
		c.Scopes = scopes
	}
	return c, nil
}

// refresh exchanges the refresh token (rotated tokens are stored).
func (f *Flow) refresh(ctx context.Context, meta asMeta, c *Credential) error {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken}, "client_id": {c.ClientID}, "resource": {f.Resource}}
	if err := f.token(ctx, meta, form, c.ClientSecret, c); err != nil {
		return err
	}
	return f.Store.Put(*c)
}

func (f *Flow) token(ctx context.Context, meta asMeta, form url.Values, secret string, c *Credential) error {
	req, err := http.NewRequestWithContext(ctx, "POST", meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if secret != "" {
		req.SetBasicAuth(url.QueryEscape(form.Get("client_id")), url.QueryEscape(secret))
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return fmt.Errorf("token endpoint: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 || t.AccessToken == "" {
		return fmt.Errorf("token endpoint: %s %s (HTTP %d)", t.Error, t.Description, resp.StatusCode) // never the token
	}
	c.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		c.RefreshToken = t.RefreshToken
	}
	c.Expiry = time.Time{}
	if t.ExpiresIn > 0 {
		c.Expiry = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	if t.Scope != "" {
		c.Scopes = strings.Fields(t.Scope)
	}
	return nil
}

func (f *Flow) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (f *Flow) postJSON(ctx context.Context, u string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sep(u string) string {
	if strings.Contains(u, "?") {
		return "&"
	}
	return "?"
}

func union(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
