// Package capability notices when a task needs something ternly doesn't
// have (an MCP server, plugin, skill or extension), finds candidates in a
// local, offline-searchable catalog, ranks them, and suggests at most one
// list per need at a turn boundary (ADR 011, requirement 9).
package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	mu    sync.Mutex
	state refreshState
}

type refreshState struct {
	Last         time.Time `json:"last"`
	RegistryAt   string    `json:"registry_at"` // updated_since cursor for deltas
	Counts       map[string]int
	LastErrors   []string
	RegistryDone bool `json:"registry_done"` // a full crawl completed
}

func (c *Catalog) open() error {
	c.once.Do(func() {
		if err := os.MkdirAll(c.Dir, 0o700); err != nil {
			c.err = err
			return
		}
		c.store, c.err = memory.OpenStore(filepath.Join(c.Dir, "catalog.log"))
		if b, err := os.ReadFile(filepath.Join(c.Dir, "state.json")); err == nil {
			_ = json.Unmarshal(b, &c.state)
		}
	})
	return c.err
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
	for _, e := range es {
		c.store.Upsert(toItem(e))
	}
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
	if c.open() != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.state.Last) > every || !c.state.RegistryDone
}

// Refresh fetches every source; each failure is recorded and the rest go on.
// The MCP registry is crawled in full once, then by updated_since deltas.
func (c *Catalog) Refresh(ctx context.Context, progress func(string)) error {
	if err := c.open(); err != nil {
		return err
	}
	if progress == nil {
		progress = func(string) {}
	}
	c.mu.Lock()
	st := c.state
	c.mu.Unlock()
	st.Counts, st.LastErrors = map[string]int{}, nil
	record := func(src string, n int, err error) {
		st.Counts[src] += n
		if err != nil {
			st.LastErrors = append(st.LastErrors, src+": "+err.Error())
		}
	}
	for _, u := range c.Sources.Marketplaces {
		n, err := c.fetchMarketplace(ctx, u)
		record("marketplaces", n, err)
		progress(fmt.Sprintf("catalog: marketplaces %d", st.Counts["marketplaces"]))
	}
	if c.Sources.GeminiIndex != "" {
		n, err := c.fetchGemini(ctx, c.Sources.GeminiIndex)
		record("gemini", n, err)
		progress(fmt.Sprintf("catalog: gemini extensions %d", n))
	}
	if c.Sources.MCPRegistry != "" {
		since := ""
		if st.RegistryDone {
			since = st.RegistryAt
		}
		started := time.Now().UTC().Format(time.RFC3339)
		n, err := c.fetchRegistry(ctx, c.Sources.MCPRegistry, since, func(n int) { progress(fmt.Sprintf("catalog: MCP registry %d", n)) })
		record("mcp-registry", n, err)
		if err == nil {
			st.RegistryDone, st.RegistryAt = true, started
		}
	}
	if c.Sources.NPMSearch {
		n, err := c.fetchNPM(ctx)
		record("npm", n, err)
	}
	st.Last = time.Now()
	c.mu.Lock()
	c.state = st
	c.mu.Unlock()
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(filepath.Join(c.Dir, "state.json"), b, 0o600)
	c.store.Sync()
	if len(st.LastErrors) > 0 {
		return fmt.Errorf("%s", strings.Join(st.LastErrors, "; "))
	}
	return nil
}

func (c *Catalog) get(ctx context.Context, u string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ternly (capability catalog)")
	cl := c.HTTP
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("%s: HTTP %d %s", u, resp.StatusCode, b)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
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
	if err := c.get(ctx, rawURL, &mk); err != nil {
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
	return len(es), c.Put(es...)
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
	if err := c.get(ctx, u, &list); err != nil {
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
	return len(es), c.Put(es...)
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

func (c *Catalog) fetchRegistry(ctx context.Context, base, since string, progress func(int)) (int, error) {
	n := 0
	cursor := ""
	for page := 0; page < 1000; page++ {
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
		if err := c.get(ctx, strings.TrimRight(base, "/")+"/v0.1/servers?"+q.Encode(), &res); err != nil {
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
		if cursor = res.Metadata.NextCursor; cursor == "" {
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
func (c *Catalog) fetchNPM(ctx context.Context) (int, error) {
	n := 0
	for from := 0; from < 1000; from += 250 {
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
		if err := c.get(ctx, fmt.Sprintf("https://registry.npmjs.org/-/v1/search?text=keywords:mcp-server&size=250&from=%d", from), &res); err != nil {
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
