package netguard

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func host(s *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(s.URL, "http://"), "https://")
}

func get(c *http.Client, u string) error {
	resp, err := c.Get(u)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return err
}

func TestGrantedAndRefusedHosts(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	c := Client(Grant{Hosts: []string{host(ok)}, AllowPrivate: true}, 5*time.Second)
	if err := get(c, ok.URL); err != nil {
		t.Fatal(err)
	}
	// Same IP, another port: not granted (host:port is exact).
	hits := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer other.Close()
	if err := get(c, other.URL); !errors.Is(err, ErrNotGranted) || !strings.Contains(err.Error(), host(other)) || hits != 0 {
		t.Fatalf("other port: %v (hits %d)", err, hits)
	}
	// A bare host allows any port.
	if err := get(Client(Grant{Hosts: []string{"127.0.0.1"}, AllowPrivate: true}, 5*time.Second), other.URL); err != nil || hits != 1 {
		t.Fatalf("bare host: %v (hits %d)", err, hits)
	}
	// Case-insensitive; default ports.
	g := Grant{Hosts: []string{"MCP.Example.com:443"}}
	for u, want := range map[string]bool{"https://mcp.example.com/x": true, "https://mcp.example.com:443/": true, "http://mcp.example.com/": false, "https://evil.example.com/": false, "ftp://mcp.example.com/": false} {
		req, _ := http.NewRequest("GET", u, nil)
		if got := newGuard(g).check(req.URL) == nil; got != want {
			t.Errorf("%s: allowed=%v", u, got)
		}
	}
}

func TestRedirects(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/end", http.StatusFound)
		case "/away":
			http.Redirect(w, r, target.URL, http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default: // /hops/N redirects N more times
			if n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hops/")); err == nil && n > 0 {
				http.Redirect(w, r, "/hops/"+strconv.Itoa(n-1), http.StatusFound)
			}
		}
	}))
	defer src.Close()
	c := Client(Grant{Hosts: []string{host(src)}, AllowPrivate: true}, 5*time.Second)
	if err := get(c, src.URL+"/same"); err != nil {
		t.Fatal(err)
	}
	if err := get(c, src.URL+"/away"); !errors.Is(err, ErrNotGranted) || hits != 0 {
		t.Fatalf("redirect away: %v (target hit %d times)", err, hits)
	}
	if err := get(c, src.URL+"/hops/5"); err != nil {
		t.Fatalf("5 redirects: %v", err)
	}
	if err := get(c, src.URL+"/hops/6"); err == nil {
		t.Fatal("6 redirects followed")
	}
	if err := get(c, src.URL+"/loop"); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("loop: %v", err)
	}
	// Approved later: followed.
	if err := Allow(c, host(target)); err != nil {
		t.Fatal(err)
	}
	if err := get(c, src.URL+"/away"); err != nil || hits != 1 {
		t.Fatalf("after Allow: %v %d", err, hits)
	}
	// Granted target too: followed.
	both := Client(Grant{Hosts: []string{host(src), host(target)}, AllowPrivate: true}, 5*time.Second)
	if err := get(both, src.URL+"/away"); err != nil || hits != 2 {
		t.Fatalf("granted redirect: %v %d", err, hits)
	}
}

func TestHTTPSToHTTPRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("downgraded") }))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tls.Close()
	c := Client(Grant{Hosts: []string{"127.0.0.1"}, AllowPrivate: true}, 5*time.Second)
	c.Transport.(*grantedTransport).next.(*http.Transport).TLSClientConfig = tls.Client().Transport.(*http.Transport).TLSClientConfig
	if err := get(c, tls.URL); !errors.Is(err, ErrNotGranted) || !strings.Contains(err.Error(), "https to http") {
		t.Fatalf("%v", err)
	}
}

func TestPrivateAddressesRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("connected") }))
	defer srv.Close()
	// Granted, but loopback: refused at dial time.
	if err := get(Client(Grant{Hosts: []string{host(srv)}}, 5*time.Second), srv.URL); !errors.Is(err, ErrNotGranted) || !strings.Contains(err.Error(), "private") {
		t.Fatalf("loopback: %v", err)
	}
	// The metadata service: refused without a packet sent.
	c := Client(Grant{Hosts: []string{"169.254.169.254"}}, 2*time.Second)
	if err := get(c, "http://169.254.169.254/latest/meta-data/"); !errors.Is(err, ErrNotGranted) {
		t.Fatalf("metadata: %v", err)
	}
	// A public name that resolves to loopback (rebinding): the check is on the dialed address.
	tr := Client(Grant{Hosts: []string{"rebind.example"}}, 5*time.Second)
	d := &net.Dialer{Control: newGuard(Grant{}).control}
	tr.Transport.(*grantedTransport).next.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return d.DialContext(ctx, network, host(srv)) // what a rebinding resolver would return
	}
	if err := get(tr, "http://rebind.example/"); !errors.Is(err, ErrNotGranted) {
		t.Fatalf("rebinding: %v", err)
	}
	for a, want := range map[string]bool{"127.0.0.1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true, "169.254.169.254": true, "0.0.0.0": true,
		"224.0.0.1": true, "::1": true, "fe80::1": true, "fd00::1": true, "::ffff:127.0.0.1": true, "ff02::1": true, "::": true,
		"8.8.8.8": false, "1.1.1.1": false, "2606:4700::1111": false, "172.32.0.1": false} {
		if got := refusedIP(netip.MustParseAddr(a).AsSlice()); got != want {
			t.Errorf("private(%s) = %v", a, got)
		}
	}
}
