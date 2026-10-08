package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// A registry page lists versions: the same servers come back on every page.
func fakeRegistry(t *testing.T, servers, pages int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		var out struct {
			Servers  []map[string]any  `json:"servers"`
			Metadata map[string]string `json:"metadata"`
		}
		for i := range servers {
			out.Servers = append(out.Servers, map[string]any{"server": map[string]any{
				"name": fmt.Sprintf("io.example/s%d", i), "description": fmt.Sprintf("postgres tools, build %d", p), "version": fmt.Sprintf("1.%d.0", p),
				"packages": []map[string]any{{"registryType": "npm", "identifier": fmt.Sprintf("s%d", i), "version": fmt.Sprintf("1.%d.0", p)}}}})
		}
		out.Metadata = map[string]string{}
		if p+1 < pages {
			out.Metadata["nextCursor"] = strconv.Itoa(p + 1)
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

// ADR 028: a refresh rebuilds the catalog's index once, at its end, however
// many entries it replaces; without the hold the same refresh rebuilds it
// repeatedly.
func TestRefreshRebuildsIndexOnce(t *testing.T) {
	reg := fakeRegistry(t, 100, 30)
	defer reg.Close()
	c := &Catalog{Dir: t.TempDir(), Sources: Sources{MCPRegistry: reg.URL}}
	defer c.Close()
	for refresh := 1; refresh <= 2; refresh++ {
		c.state.RegistryDone = false // a full crawl each time
		if err := c.open(); err != nil {
			t.Fatal(err)
		}
		before := c.store.IndexRebuilds()
		if err := c.Refresh(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if n := c.store.IndexRebuilds() - before; n != 1 {
			t.Fatalf("refresh %d: %d index rebuilds, want 1", refresh, n)
		}
		if c.Len() != 100 {
			t.Fatalf("refresh %d: %d entries", refresh, c.Len())
		}
	}
}
