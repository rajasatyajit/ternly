// Package capability notices when a task needs something ternly doesn't
// have (an MCP server, plugin, skill or extension), finds candidates in a
// local, offline-searchable catalog, ranks them, and suggests at most one
// list per need at a turn boundary (ADR 011, requirement 9).
package capability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rajasatyajit/ternly/internal/memory"
)

// Entry is one installable capability.
type Entry struct {
	ID          string // kind:name, unique
	Kind        string // mcp, plugin, extension, npm
	Name        string
	Description string
	Publisher   string
	Source      string // where it was listed: mcp-registry, marketplace:<name>, gemini-gallery, npm
	URL         string // repository or homepage
	Install     string // what /plugin add or the MCP installer takes
	Version     string // pinned version (or commit, once fetched)
	License     string
	Updated     string // RFC 3339
	Popularity  int    // stars or weekly downloads
	Official    bool   // from source metadata: Anthropic's official marketplace, Google-owned in Gemini's gallery
	Verified    bool   // the registry verified the publisher's namespace
	Runs        string // what it executes, as listed
	Network     bool   // needs the network (remote services, package download)
	Coverage    float64
	Secrets     []string // environment variables it needs, as declared
	Tokens      int      // estimated context cost
}

// Sources are the catalogs, as verified on 2026-10-05 (ADR 011). Users can
// add their own in config.
type Sources struct {
	MCPRegistry  string   // base URL of an MCP registry (official: registry.modelcontextprotocol.io)
	Marketplaces []string // raw marketplace.json URLs
	GeminiIndex  string   // geminicli.com/extensions.json
	NPMSearch    bool     // npm packages with the mcp-server keyword (noisy; low trust)
}

// DefaultSources: Anthropic's documented marketplaces, the official MCP
// registry, Gemini CLI's default extension registry, npm.
var DefaultSources = Sources{
	MCPRegistry: "https://registry.modelcontextprotocol.io",
	Marketplaces: []string{
		"https://raw.githubusercontent.com/anthropics/claude-plugins-official/main/.claude-plugin/marketplace.json",
		"https://raw.githubusercontent.com/anthropics/claude-plugins-community/main/.claude-plugin/marketplace.json",
		"https://raw.githubusercontent.com/anthropics/claude-code/main/.claude-plugin/marketplace.json",
		"https://raw.githubusercontent.com/anthropics/skills/main/.claude-plugin/marketplace.json",
		"https://raw.githubusercontent.com/anthropics/knowledge-work-plugins/main/.claude-plugin/marketplace.json",
	},
	GeminiIndex: "https://geminicli.com/extensions.json",
	NPMSearch:   true,
}

// Catalog is the local index: a memory.Store (ternly's own log-backed
// store, requirement 4) of entries, refreshed in the background.
type Catalog struct {
	Dir     string
	Sources Sources
	HTTP    *http.Client

	once  sync.Once
	store *memory.Store
	err   error

	stateOnce  sync.Once
	mu         sync.Mutex
	state      refreshState
	refreshing sync.Mutex  // one refresh at a time
	hurry      atomic.Bool // a refresh the user asked for waits: the idle one stops waiting for idle
	wire       atomic.Int64
	counting   *http.Client
}

type refreshState struct {
	Last         time.Time `json:"last"`
	RegistryAt   string    `json:"registry_at"` // updated_since cursor for deltas
	Counts       map[string]int
	LastErrors   []string
	RegistryDone bool `json:"registry_done"` // a full crawl completed
	// A full crawl interrupted (ternly quit) goes on from its cursor; its
	// start becomes RegistryAt once it completes (ADR 031).
	RegistryCursor  string `json:"registry_cursor,omitempty"`
	RegistryStarted string `json:"registry_started,omitempty"`
	// Validators per URL: sent back as If-None-Match / If-Modified-Since, so
	// an unchanged source is a 304 with no body; the body's hash catches the
	// sources that send neither (ADR 031). Kept only once its entries are in.
	Validators map[string]validator `json:"validators,omitempty"`
	NPMAt      time.Time            `json:"npm_at,omitempty"` // npm sends no validators: searched weekly
	Bytes      int64                `json:"bytes"`            // received on the wire by the last refresh
	Unchanged  int                  `json:"unchanged"`        // requests the last refresh skipped (304 or same hash)
}

type validator struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	Hash         string `json:"sha256,omitempty"`
}

func (c *Catalog) open() error {
	c.once.Do(func() {
		if err := os.MkdirAll(c.Dir, 0o700); err != nil {
			c.err = err
			return
		}
		c.store, c.err = memory.OpenStore(filepath.Join(c.Dir, "catalog.log"))
	})
	return c.err
}

// loadState reads state.json once; it doesn't open the store (Due at
// startup costs a small file read, not the index).
func (c *Catalog) loadState() {
	c.stateOnce.Do(func() {
		if b, err := os.ReadFile(filepath.Join(c.Dir, "state.json")); err == nil {
			c.mu.Lock()
			_ = json.Unmarshal(b, &c.state)
			c.mu.Unlock()
		}
	})
}

func (c *Catalog) saveState() {
	c.mu.Lock()
	b, err := json.MarshalIndent(c.state, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return
	}
	tmp := filepath.Join(c.Dir, "state.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(c.Dir, "state.json"))
	}
}

// Len is the number of entries.
func (c *Catalog) Len() int {
	if c.open() != nil {
		return 0
	}
	return c.store.Len()
}

// Close closes the store.
func (c *Catalog) Close() error {
	if c.store != nil {
		return c.store.Close()
	}
	return nil
}

// Put adds or replaces entries (fetchers, tests).
func (c *Catalog) Put(es ...Entry) error {
	if err := c.open(); err != nil {
		return err
	}
	items := make([]*memory.Item, len(es))
	for i, e := range es {
		items[i] = toItem(e)
	}
	c.store.UpsertAll(items) // one lock per page
	return nil
}

func toItem(e Entry) *memory.Item {
	meta := map[string]string{"kind": e.Kind, "name": e.Name, "publisher": e.Publisher, "source": e.Source, "url": e.URL, "install": e.Install,
		"version": e.Version, "license": e.License, "updated": e.Updated, "popularity": strconv.Itoa(e.Popularity), "runs": e.Runs,
		"coverage": strconv.FormatFloat(e.Coverage, 'f', 2, 64), "tokens": strconv.Itoa(e.Tokens), "secrets": strings.Join(e.Secrets, ",")}
	if e.Official {
		meta["official"] = "1"
	}
	if e.Verified {
		meta["verified"] = "1"
	}
	if e.Network {
		meta["network"] = "1"
	}
	now := time.Now().UnixMilli()
	return &memory.Item{ID: e.ID, Scope: memory.Project, Kind: "catalog", Text: e.Name + " — " + e.Description, Keys: []string{strings.ReplaceAll(e.Name, "-", " ")},
		Source: "auto", Created: now, Updated: now, Meta: meta}
}

func fromItem(it memory.Item) Entry {
	m := it.Meta
	pop, _ := strconv.Atoi(m["popularity"])
	cov, _ := strconv.ParseFloat(m["coverage"], 64)
	tok, _ := strconv.Atoi(m["tokens"])
	desc := it.Text
	if _, d, ok := strings.Cut(it.Text, " — "); ok {
		desc = d
	}
	var secrets []string
	if m["secrets"] != "" {
		secrets = strings.Split(m["secrets"], ",")
	}
	return Entry{ID: it.ID, Kind: m["kind"], Name: m["name"], Description: desc, Publisher: m["publisher"], Source: m["source"], URL: m["url"],
		Install: m["install"], Version: m["version"], License: m["license"], Updated: m["updated"], Popularity: pop, Runs: m["runs"],
		Official: m["official"] == "1", Verified: m["verified"] == "1", Network: m["network"] == "1", Coverage: cov, Tokens: tok, Secrets: secrets}
}

// Due reports whether a refresh is due (daily by default).
func (c *Catalog) Due(every time.Duration) bool {
	c.loadState()
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.state.Last) > every || !c.state.RegistryDone
}

// npmBase is npm's registry (a variable for tests).
var npmBase = "https://registry.npmjs.org"

// NPMEvery: npm's search sends no validators, so each search downloads its
// pages (about 1.3 MB); weekly is enough for a low-trust source.
const NPMEvery = 7 * 24 * time.Hour

// Refresh fetches every source now (the user asked: /plugin catalog
// refresh); each failure is recorded and the rest go on. The MCP registry is
// crawled in full once, then by updated_since deltas; unchanged sources are
// not downloaded again (304) or not re-indexed (same hash).
func (c *Catalog) Refresh(ctx context.Context, progress func(string)) error {
	c.hurry.Store(true) // an idle refresh in progress stops waiting and finishes first
	c.refreshing.Lock()
	defer c.refreshing.Unlock()
	c.hurry.Store(false)
	return c.refresh(ctx, nil, progress)
}

// RefreshIdle is the background refresh: before each request it waits
// until allowed (the session idle, on AC power: ADR 031), so the user's
// return pauses it after the request in flight; what it fetched is kept.
func (c *Catalog) RefreshIdle(ctx context.Context, allowed func() bool) error {
	c.refreshing.Lock()
	defer c.refreshing.Unlock()
	return c.refresh(ctx, allowed, nil)
}

// idlePoll is how often a paused refresh looks again.
var idlePoll = 5 * time.Second

func (c *Catalog) refresh(ctx context.Context, allowed func() bool, progress func(string)) error {
	if err := c.open(); err != nil {
		return err
	}
	c.loadState()
	if progress == nil {
		progress = func(string) {}
	}
	if c.store.Len() == 0 { // an emptied index fetches everything: validators describe what it had
		c.mu.Lock()
		c.state.Validators = nil
		c.mu.Unlock()
	}
	wait := func() error {
		for allowed != nil && !c.hurry.Load() && !allowed() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(idlePoll):
			}
		}
		return ctx.Err()
	}
	defer c.store.HoldIndex()() // the index rebuilds at most once per refresh, at the end
	c.wire.Store(0)
	counts, unchanged := map[string]int{}, 0
	var errs []string
	record := func(src string, n int, err error) {
		counts[src] += n
		if err != nil && !errors.Is(err, errUnchanged) {
			errs = append(errs, src+": "+err.Error())
		}
		if errors.Is(err, errUnchanged) {
			unchanged++
		}
		c.saveState() // what's in is kept if ternly quits mid-refresh
	}
	for _, u := range c.Sources.Marketplaces {
		if wait() != nil {
			return ctx.Err()
		}
		n, err := c.fetchMarketplace(ctx, u)
		record("marketplaces", n, err)
		progress(fmt.Sprintf("catalog: marketplaces %d", counts["marketplaces"]))
	}
	if c.Sources.GeminiIndex != "" {
		if wait() != nil {
			return ctx.Err()
		}
		n, err := c.fetchGemini(ctx, c.Sources.GeminiIndex)
		record("gemini", n, err)
		progress(fmt.Sprintf("catalog: gemini extensions %d", n))
	}
	if c.Sources.MCPRegistry != "" {
		c.mu.Lock()
		since, cursor := "", ""
		if c.state.RegistryDone {
			since = c.state.RegistryAt
		} else if cursor = c.state.RegistryCursor; cursor == "" {
			c.state.RegistryStarted = time.Now().UTC().Format(time.RFC3339)
		}
		started := c.state.RegistryStarted
		if since != "" {
			started = time.Now().UTC().Format(time.RFC3339)
		}
		c.mu.Unlock()
		n, err := c.fetchRegistry(ctx, c.Sources.MCPRegistry, since, cursor, wait, func(n int) { progress(fmt.Sprintf("catalog: MCP registry %d", n)) })
		if err == nil {
			c.mu.Lock()
			c.state.RegistryDone, c.state.RegistryAt, c.state.RegistryCursor, c.state.RegistryStarted = true, started, "", ""
			c.mu.Unlock()
		}
		record("mcp-registry", n, err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	c.mu.Lock()
	npmDue := time.Since(c.state.NPMAt) > NPMEvery
	c.mu.Unlock()
	if c.Sources.NPMSearch && npmDue {
		n, err := c.fetchNPM(ctx, wait)
		if err == nil {
			c.mu.Lock()
			c.state.NPMAt = time.Now()
			c.mu.Unlock()
		}
		record("npm", n, err)
	}
	if ctx.Err() != nil {
		return ctx.Err() // not a completed refresh: due again next start, from where it was
	}
	c.mu.Lock()
	c.state.Last, c.state.Counts, c.state.LastErrors, c.state.Bytes, c.state.Unchanged = time.Now(), counts, errs, c.wire.Load(), unchanged
	c.mu.Unlock()
	c.saveState()
	c.store.Sync()
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// LastRefresh reports the last completed refresh: when, the bytes it
// received and the requests it skipped as unchanged.
func (c *Catalog) LastRefresh() (at time.Time, bytes int64, unchanged int) {
	c.loadState()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Last, c.state.Bytes, c.state.Unchanged
}

// errUnchanged: the source is as last indexed (a 304, or the same body).
var errUnchanged = errors.New("unchanged")

// maxBody bounds a source's response.
const maxBody = 32 << 20

// get fetches u into v. With validators from an earlier fetch (and entries
// in the index) it asks conditionally; it returns errUnchanged for a 304 or
// a body identical to the last one indexed. commit keeps the new validators:
// the caller calls it once the entries are in the index.
func (c *Catalog) get(ctx context.Context, u string, v any) (commit func(), err error) {
	return c.fetch(ctx, u, v, true)
}

// fetch is get; remember false keeps no validators for u (a URL that is
// never asked again: the registry's pages carry a cursor or a timestamp).
func (c *Catalog) fetch(ctx context.Context, u string, v any, remember bool) (commit func(), err error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ternly (capability catalog)")
	c.mu.Lock()
	old, known := c.state.Validators[u]
	c.mu.Unlock()
	if known {
		if old.ETag != "" {
			req.Header.Set("If-None-Match", old.ETag)
		}
		if old.LastModified != "" {
			req.Header.Set("If-Modified-Since", old.LastModified)
		}
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && known {
		return nil, errUnchanged
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("%s: HTTP %d %s", u, resp.StatusCode, b)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%s: over %d MB", u, maxBody>>20)
	}
	sum := sha256.Sum256(body)
	nv := validator{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), Hash: hex.EncodeToString(sum[:])}
	commit = func() {
		if !remember {
			return
		}
		c.mu.Lock()
		if c.state.Validators == nil {
			c.state.Validators = map[string]validator{}
		}
		c.state.Validators[u] = nv
		c.mu.Unlock()
	}
	if remember && known && old.Hash == nv.Hash {
		commit()
		return nil, errUnchanged
	}
	if err := json.Unmarshal(body, v); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return commit, nil
}

// client counts the bytes received on the wire (TLS included) for
// LastRefresh, unless a client was set.
func (c *Catalog) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counting == nil {
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &countingConn{Conn: conn, n: &c.wire}, nil
		}
		c.counting = &http.Client{Transport: tr}
	}
	return c.counting
}

type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (cc *countingConn) Read(b []byte) (int, error) {
	n, err := cc.Conn.Read(b)
	cc.n.Add(int64(n))
	return n, err
}

// ─────────────────────────── Claude Code marketplaces ───────────────────────────

func (c *Catalog) fetchMarketplace(ctx context.Context, rawURL string) (int, error) {
	var mk struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Version     string          `json:"version"`
			Category    string          `json:"category"`
			Homepage    string          `json:"homepage"`
			Author      json.RawMessage `json:"author"`
			Source      json.RawMessage `json:"source"`
			LSP         json.RawMessage `json:"lspServers"`
			MCP         json.RawMessage `json:"mcpServers"`
		} `json:"plugins"`
	}
	commit, err := c.get(ctx, rawURL, &mk)
	if err != nil {
		return 0, err
	}
	repo := repoOfRaw(rawURL)
	official := mk.Name == "claude-plugins-official" && strings.EqualFold(repo, "anthropics/claude-plugins-official")
	var es []Entry
	for _, p := range mk.Plugins {
		var a struct{ Name string }
		_ = json.Unmarshal(p.Author, &a)
		e := Entry{ID: "plugin:" + p.Name + "@" + mk.Name, Kind: "plugin", Name: p.Name, Description: strings.TrimSpace(p.Description + " " + p.Category),
			Publisher: orStr(a.Name, mk.Name), Source: "marketplace:" + mk.Name, URL: orStr(p.Homepage, "https://github.com/"+repo), Install: p.Name + "@" + mk.Name + " " + repo,
			Version: p.Version, Official: official, Runs: "plugin files (reviewed at install)", Coverage: 1, Tokens: 60}
		switch {
		case len(p.LSP) > 2 && string(p.LSP) != "null":
			e.Coverage, e.Runs = 0, "a language server (not supported by ternly)"
		case len(p.MCP) > 2 && string(p.MCP) != "null":
			e.Runs, e.Network = "an MCP server", true
		}
		es = append(es, e)
	}
	return c.putAll(es, commit)
}

// repoOfRaw: owner/repo of a raw.githubusercontent.com URL.
func repoOfRaw(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host != "raw.githubusercontent.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(p.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// ─────────────────────────── Gemini CLI gallery ───────────────────────────

func (c *Catalog) fetchGemini(ctx context.Context, u string) (int, error) {
	var list []struct {
		ID, URL, FullName, RepoDescription, LastUpdated, ExtensionName, ExtensionVersion, ExtensionDescription, LicenseKey string
		Stars                                                                                                              int
		HasMCP, HasHooks, HasSkills, HasCustomCommands, HasContext, IsGoogleOwned                                          bool
	}
	commit, err := c.get(ctx, u, &list)
	if err != nil {
		return 0, err
	}
	var es []Entry
	for _, x := range list {
		var runs []string
		if x.HasMCP {
			runs = append(runs, "MCP server")
		}
		if x.HasHooks {
			runs = append(runs, "hooks")
		}
		if x.HasSkills {
			runs = append(runs, "skills")
		}
		if x.HasCustomCommands {
			runs = append(runs, "commands")
		}
		owner, _, _ := strings.Cut(x.FullName, "/")
		es = append(es, Entry{ID: "gemini:" + x.FullName, Kind: "extension", Name: x.ExtensionName, Description: orStr(x.ExtensionDescription, x.RepoDescription),
			Publisher: owner, Source: "gemini-gallery", URL: x.URL, Install: x.URL, Version: x.ExtensionVersion, License: x.LicenseKey, Updated: x.LastUpdated,
			Popularity: x.Stars, Official: x.IsGoogleOwned, Runs: orStr(strings.Join(runs, ", "), "context only"), Network: x.HasMCP, Coverage: 1, Tokens: 80})
	}
	return c.putAll(es, commit)
}

// putAll indexes a source's entries, then keeps its validators.
func (c *Catalog) putAll(es []Entry, commit func()) (int, error) {
	if err := c.Put(es...); err != nil {
		return 0, err
	}
	commit()
	return len(es), nil
}

// ─────────────────────────── MCP registry ───────────────────────────

type regServer struct {
	Server struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Version     string `json:"version"`
		WebsiteURL  string `json:"websiteUrl"`
		Repository  struct {
			URL string `json:"url"`
		} `json:"repository"`
		Packages []struct {
			RegistryType string `json:"registryType"`
			Identifier   string `json:"identifier"`
			Version      string `json:"version"`
			RuntimeHint  string `json:"runtimeHint"`
			Transport    struct {
				Type string `json:"type"`
			} `json:"transport"`
			Env []struct {
				Name       string `json:"name"`
				IsRequired bool   `json:"isRequired"`
				IsSecret   bool   `json:"isSecret"`
			} `json:"environmentVariables"`
		} `json:"packages"`
		Remotes []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"remotes"`
	} `json:"server"`
	Meta map[string]struct {
		Status    string `json:"status"`
		UpdatedAt string `json:"updatedAt"`
	} `json:"_meta"`
}

// fetchRegistry crawls the registry from cursor ("": the start), or the
// changes since since. The registry sends no validators; a full crawl's
// cursor is saved per page, so an interrupted crawl goes on from there.
func (c *Catalog) fetchRegistry(ctx context.Context, base, since, cursor string, wait func() error, progress func(int)) (int, error) {
	n := 0
	for page := 0; page < 1000; page++ {
		if err := wait(); err != nil {
			return n, err
		}
		q := url.Values{"version": {"latest"}, "limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if since != "" {
			q.Set("updated_since", since)
		}
		var res struct {
			Servers  []regServer `json:"servers"`
			Metadata struct {
				NextCursor string `json:"nextCursor"`
			} `json:"metadata"`
		}
		if _, err := c.fetch(ctx, strings.TrimRight(base, "/")+"/v0.1/servers?"+q.Encode(), &res, false); err != nil {
			if cursor != "" && since == "" && page == 0 { // a saved cursor the registry no longer takes: crawl from the start next time
				c.mu.Lock()
				c.state.RegistryCursor = ""
				c.mu.Unlock()
			}
			return n, err
		}
		var es []Entry
		for _, s := range res.Servers {
			if e, ok := registryEntry(s); ok {
				es = append(es, e)
			} else if since != "" { // deleted or deprecated since the last crawl
				_ = c.store.Forget("mcp:" + s.Server.Name)
			}
		}
		if err := c.Put(es...); err != nil {
			return n, err
		}
		n += len(es)
		progress(n)
		cursor = res.Metadata.NextCursor
		if since == "" { // a full crawl: go on from here if interrupted
			c.mu.Lock()
			c.state.RegistryCursor = cursor
			c.mu.Unlock()
			c.saveState()
		}
		if cursor == "" {
			return n, nil
		}
	}
	return n, nil
}

func registryEntry(s regServer) (Entry, bool) {
	sv := s.Server
	off := s.Meta["io.modelcontextprotocol.registry/official"]
	if off.Status == "deleted" || off.Status == "deprecated" {
		return Entry{}, false
	}
	ns, _, _ := strings.Cut(sv.Name, "/")
	e := Entry{ID: "mcp:" + sv.Name, Kind: "mcp", Name: sv.Name, Description: strings.TrimSpace(sv.Title + " " + sv.Description), Publisher: ns,
		Source: "mcp-registry", URL: orStr(sv.Repository.URL, sv.WebsiteURL), Version: sv.Version, Updated: off.UpdatedAt,
		Verified: true, Tokens: 400} // the registry verifies namespace ownership (GitHub, DNS or HTTP)
	for _, p := range sv.Packages {
		if p.Transport.Type != "stdio" && p.Transport.Type != "" {
			continue
		}
		for _, v := range p.Env {
			if v.IsRequired || v.IsSecret {
				e.Secrets = append(e.Secrets, v.Name)
			}
		}
		switch p.RegistryType {
		case "npm":
			e.Install, e.Runs, e.Coverage, e.Network = "npm:"+p.Identifier+"@"+p.Version, "npx -y "+p.Identifier+"@"+p.Version, 1, true
		case "pypi":
			e.Install, e.Runs, e.Coverage, e.Network = "pypi:"+p.Identifier+"=="+p.Version, "uvx "+p.Identifier+"=="+p.Version, 1, true
		case "oci":
			e.Runs, e.Coverage = "docker image "+p.Identifier+" (containers can't run inside ternly's sandbox)", 0
		default:
			e.Runs, e.Coverage = p.RegistryType+" package (not supported)", 0
		}
		if e.Coverage > 0 {
			break
		}
	}
	if e.Install == "" && len(sv.Remotes) > 0 {
		e.Runs, e.Coverage, e.Network = "remote server at "+sv.Remotes[0].URL+" (remote MCP isn't supported yet)", 0, true
	}
	return e, true
}

// ─────────────────────────── npm ───────────────────────────

// fetchNPM pages through the mcp-server keyword (capped: the search is noisy
// and rate-limited; registry-listed servers are better sourced above).
func (c *Catalog) fetchNPM(ctx context.Context, wait func() error) (int, error) {
	n := 0
	for from := 0; from < 1000; from += 250 {
		if err := wait(); err != nil {
			return n, err
		}
		var res struct {
			Total   int `json:"total"`
			Objects []struct {
				Downloads struct {
					Weekly int `json:"weekly"`
				} `json:"downloads"`
				Updated string `json:"updated"`
				Package struct {
					Name, Version, Description, License string
					Links                               struct{ Repository, Homepage string }
					Publisher                           struct{ Username string }
				} `json:"package"`
			} `json:"objects"`
		}
		commit, err := c.get(ctx, fmt.Sprintf("%s/-/v1/search?text=keywords:mcp-server&size=250&from=%d", npmBase, from), &res)
		switch {
		case errors.Is(err, errUnchanged): // the same page as last time: nothing to index
			res.Total = 1000 // unknown without decoding: the next page says
			commit = func() {}
		case err != nil:
			return n, err
		}
		var es []Entry
		for _, o := range res.Objects {
			p := o.Package
			es = append(es, Entry{ID: "npm:" + p.Name, Kind: "npm", Name: p.Name, Description: p.Description, Publisher: p.Publisher.Username, Source: "npm",
				URL: orStr(p.Links.Repository, p.Links.Homepage), Install: "npm:" + p.Name + "@" + p.Version, Version: p.Version, License: p.License,
				Updated: o.Updated, Popularity: o.Downloads.Weekly, Runs: "npx -y " + p.Name + "@" + p.Version, Network: true, Coverage: 1, Tokens: 400})
		}
		if err := c.Put(es...); err != nil {
			return n, err
		}
		commit()
		n += len(es)
		if from+250 >= res.Total {
			break
		}
		select { // npm rate-limits quick paging
		case <-ctx.Done():
			return n, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return n, nil
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
