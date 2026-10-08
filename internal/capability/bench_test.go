package capability

import (
	"fmt"
	"testing"
)

// BenchmarkCatalogPut: a refresh's writes, 1000 entries replaced 8 times in
// pages of 100 (the registry's page size), through Catalog.Put.
func BenchmarkCatalogPut(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		c := &Catalog{Dir: b.TempDir()}
		for round := range 8 {
			for page := range 10 {
				es := make([]Entry, 100)
				for i := range es {
					n := page*100 + i
					es[i] = Entry{ID: fmt.Sprintf("mcp:s%d", n), Kind: "mcp", Name: fmt.Sprintf("s%d", n), Source: "mcp-registry", Coverage: 1,
						Description: fmt.Sprintf("an MCP server for postgres queries, build %d of %d", round, n)}
				}
				if err := c.Put(es...); err != nil {
					b.Fatal(err)
				}
			}
		}
		_ = c.store.Flush()
		_ = c.Close()
	}
}
