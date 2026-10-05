package capability

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestLiveCatalog refreshes the real catalog into TERNLY_CAP_LIVE (a
// directory, kept between runs: later runs fetch only deltas) and measures
// index search latency on it.
func TestLiveCatalog(t *testing.T) {
	dir := os.Getenv("TERNLY_CAP_LIVE")
	if dir == "" {
		t.Skip("TERNLY_CAP_LIVE not set")
	}
	c := &Catalog{Dir: dir, Sources: DefaultSources}
	defer c.Close()
	t0 := time.Now()
	err := c.Refresh(context.Background(), func(s string) {})
	t.Logf("refresh: %v (%v); %d entries; counts %v", time.Since(t0).Round(time.Second), err, c.Len(), c.state.Counts)
	var lat []time.Duration
	for _, s := range Systems {
		for range 5 {
			a := time.Now()
			c.Search(Need{Key: s.Key, Label: s.Label, Query: s.Query}, 3)
			lat = append(lat, time.Since(a))
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t.Logf("search over %d entries: p50 %v p99 %v", c.Len(), lat[len(lat)/2], lat[len(lat)*99/100])
	for _, k := range []string{"postgres", "jira", "pdf", "figma", "browser", "kubernetes"} {
		s := systemOf(k)
		for i, cd := range c.Search(Need{Key: s.Key, Label: s.Label, Query: s.Query}, 3) {
			t.Logf("%-10s #%d %.2f %-40s %s | %s", k, i+1, cd.Score, cd.Name, cd.Reason, cd.Runs)
		}
	}
	fmt.Print()
}

// TestLiveRelevance: for each labelled need, is the top candidate about
// that system (its name or description names it)? Live catalog.
func TestLiveRelevance(t *testing.T) {
	dir := os.Getenv("TERNLY_CAP_LIVE")
	if dir == "" {
		t.Skip("TERNLY_CAP_LIVE not set")
	}
	c := &Catalog{Dir: dir}
	defer c.Close()
	top1, top3 := 0, 0
	for _, n := range needing {
		s := systemOf(n.key)
		cs := c.Search(Need{Key: s.Key, Label: s.Label, Query: s.Query}, 3)
		for i, cd := range cs {
			if mentions(cd.Name+" "+cd.Description, s) || strings.Contains(strings.ToLower(cd.Name+" "+cd.Description), s.Key) {
				if i == 0 {
					top1++
				}
				top3++
				break
			}
		}
		if len(cs) > 0 {
			t.Logf("%-11s → %-38s %s", n.key, cs[0].Name, cs[0].Reason)
		}
	}
	t.Logf("top-1 relevant for %d/%d needs; a relevant one in the top 3 for %d/%d", top1, len(needing), top3, len(needing))
}
