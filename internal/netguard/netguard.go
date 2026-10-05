// Package netguard hands out http.Clients that reach only what a Grant
// allows: the hosts one remote MCP server was configured with. The grant is
// enforced at the connection — the URL is checked before dialing, redirects
// only ever hop inside the grant, and the address actually dialed is checked
// after DNS — so a redirect or a DNS answer cannot widen it.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Grant is what one remote server may reach.
type Grant struct {
	Hosts        []string // host or host:port values allowed (exact match, case-insensitive; a bare host allows any port)
	AllowPrivate bool     // allow loopback/private/link-local addresses (only for servers the user configured on such an address)
}

// ErrNotGranted is returned (wrapped) for any refused request.
var ErrNotGranted = errors.New("netguard: host not granted")

// maxRedirects caps how many hops one request may follow.
const maxRedirects = 5

// Client returns an http.Client that only connects to granted hosts.
//
// timeout bounds dialing, the TLS handshake and the wait for response
// headers (<= 0 means no bound); it is not a whole-request timeout, since
// MCP streams responses for as long as the session lasts. The client never
// uses a proxy — a proxy would dial on its behalf, past the grant — and the
// transport re-checks every request, redirect hops included, so the host
// grant holds even if the client's redirect policy were swapped out. A zero
// Grant allows nothing.
func Client(g Grant, timeout time.Duration) *http.Client {
	gr := newGuard(g)
	d := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control:   gr.control, // runs on the address DNS produced, pre-connect
	}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport:     &grantedTransport{next: tr, g: gr},
		CheckRedirect: gr.redirect,
	}
}

// grantedTransport refuses ungranted requests before any connection is made,
// on the initial request and on every redirect hop.
type grantedTransport struct {
	next http.RoundTripper
	g    *guard
}

func (t *grantedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.g.check(req.URL); err != nil {
		if req.Body != nil {
			req.Body.Close() // a RoundTripper closes the body, even on error
		}
		return nil, err
	}
	return t.next.RoundTrip(req)
}

// Allow adds a host (or host:port) to the grant of a client made by
// Client: a host the user approved while a flow was under way, such as a
// server's authorization server at login.
func Allow(c *http.Client, hostport string) error {
	t, ok := c.Transport.(*grantedTransport)
	if !ok {
		return errors.New("netguard: not a netguard client")
	}
	host, port := splitHost(hostport)
	if host == "" {
		return fmt.Errorf("netguard: empty host %q", hostport)
	}
	t.g.mu.Lock()
	defer t.g.mu.Unlock()
	t.g.ports[host] = append(t.g.ports[host], port)
	return nil
}

// guard is a Grant compiled for matching: hosts folded to lowercase, each
// with the ports it allows ("" meaning any).
type guard struct {
	mu           sync.Mutex // ports grows by Allow
	ports        map[string][]string
	allowPrivate bool
}

func newGuard(g Grant) *guard {
	gr := &guard{ports: make(map[string][]string, len(g.Hosts)), allowPrivate: g.AllowPrivate}
	for _, h := range g.Hosts {
		if host, port := splitHost(h); host != "" {
			gr.ports[host] = append(gr.ports[host], port)
		}
	}
	return gr
}

// redirect is the client's redirect policy: at most maxRedirects hops, every
// hop granted, and never a downgrade from https to http.
func (g *guard) redirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("%w: too many redirects (max %d)", ErrNotGranted, maxRedirects)
	}
	if err := g.check(req.URL); err != nil {
		return err
	}
	// Only the hop's own scheme matters: downgrades were already refused, so
	// an https request anywhere in the chain means every hop since stayed https.
	if strings.EqualFold(req.URL.Scheme, "http") {
		for _, v := range via {
			if strings.EqualFold(v.URL.Scheme, "https") {
				return fmt.Errorf("%w: redirect from https to http", ErrNotGranted)
			}
		}
	}
	return nil
}

// check reports why a request to u is refused, nil when it may proceed.
func (g *guard) check(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not http or https", ErrNotGranted, u.Scheme)
	}
	if !g.granted(u.Host, scheme) {
		return fmt.Errorf("%w: %q is not in the grant", ErrNotGranted, u.Host)
	}
	return nil
}

// granted reports whether a URL host is in the grant. A bare grant host
// allows any port; a URL that spells no port out implies its scheme's
// default, so "example.com:80" grants http://example.com/.
func (g *guard) granted(hostport, scheme string) bool {
	host, port := splitHost(hostport)
	if host == "" {
		return false
	}
	if port == "" {
		port = defaultPort(scheme)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.ports[host] {
		if p == "" || p == port {
			return true
		}
	}
	return false
}

// splitHost splits a grant entry or URL host into a lowercase host and a
// port, "" when absent; IPv6 hosts keep no brackets.
func splitHost(s string) (host, port string) {
	s = strings.ToLower(s)
	if h, p, err := net.SplitHostPort(s); err == nil {
		return strings.Trim(h, "[]"), p
	}
	return strings.Trim(s, "[]"), ""
}

// defaultPort is the port a URL implies when it spells none out.
func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// control runs on every address a dial is about to open — after DNS, before
// connect — so a granted hostname that resolves to a private address (DNS
// rebinding) is refused unless the grant allows private networks.
func (g *guard) control(network, address string, c syscall.RawConn) error {
	if g.allowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: dial address %q: %v", ErrNotGranted, address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: dial address %q is not an IP", ErrNotGranted, address)
	}
	if refusedIP(ip) {
		return fmt.Errorf("%w: %s is a private or local address", ErrNotGranted, ip)
	}
	return nil
}

// refusedIP reports whether ip is in a class no grant allows without
// AllowPrivate: loopback, private (RFC 1918, fc00::/7), link-local
// (169.254.0.0/16, fe80::/10), unspecified or multicast.
func refusedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast()
}
