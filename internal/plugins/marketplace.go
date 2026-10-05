package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Marketplace is a Claude Code plugin marketplace (.claude-plugin/
// marketplace.json in a git repository), recorded with the URL it was
// actually fetched from: that, not its self-declared name, is what trust
// labels look at.
type Marketplace struct {
	Name    string             `json:"name"`
	URL     string             `json:"url"`    // as fetched
	Commit  string             `json:"commit"` // the catalog's pinned commit
	Owner   string             `json:"owner"`
	Plugins []MarketplaceEntry `json:"plugins"`
}

// MarketplaceEntry is one plugin listed in a marketplace.
type MarketplaceEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Category    string          `json:"category"`
	Tags        []string        `json:"tags"`
	Source      json.RawMessage `json:"source"`
}

// AddMarketplace fetches a marketplace (owner/repo, a git URL, or a local
// directory) and records it.
func (s *Store) AddMarketplace(ctx context.Context, spec string) (*Marketplace, error) {
	url, ref, _ := strings.Cut(spec, "#")
	if githubRepo(url) != "" && !strings.Contains(url, "://") && !strings.HasPrefix(url, "git@") {
		url = "https://github.com/" + url
	}
	dir := filepath.Join(s.Dir, ".marketplaces", randHex())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var commit string
	var err error
	if fi, statErr := os.Stat(url); statErr == nil && fi.IsDir() {
		err, commit = copyTree(url, dir), "local"
	} else {
		commit, err = gitFetch(ctx, url, ref, dir)
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("no .claude-plugin/marketplace.json in %s", spec)
	}
	var raw struct {
		Name  string `json:"name"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Plugins []MarketplaceEntry `json:"plugins"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("marketplace.json: %w", err)
	}
	if !validName.MatchString(raw.Name) {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("invalid marketplace name %q", raw.Name)
	}
	mk := &Marketplace{Name: raw.Name, URL: url, Commit: commit, Owner: raw.Owner.Name, Plugins: raw.Plugins}
	final := filepath.Join(s.Dir, ".marketplaces", raw.Name)
	os.RemoveAll(final)
	if err := os.Rename(dir, final); err != nil {
		return nil, err
	}
	all, _ := s.Marketplaces()
	kept := all[:0]
	for _, m := range all {
		if m.Name != mk.Name {
			kept = append(kept, m)
		}
	}
	kept = append(kept, *mk)
	sort.Slice(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	data, _ := json.MarshalIndent(kept, "", "  ")
	return mk, os.WriteFile(filepath.Join(s.Dir, "marketplaces.json"), data, 0o600)
}

// Marketplaces lists the recorded marketplaces.
func (s *Store) Marketplaces() ([]Marketplace, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "marketplaces.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var out []Marketplace
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(b, &out)
}

// Resolve turns "plugin@marketplace" into a pinned source. Relative entries
// resolve inside the marketplace's own repository at its recorded commit;
// github, url and git-subdir sources keep their sha or ref.
func (s *Store) Resolve(name, market string) (Source, MarketplaceEntry, error) {
	all, err := s.Marketplaces()
	if err != nil {
		return Source{}, MarketplaceEntry{}, err
	}
	for _, mk := range all {
		if mk.Name != market {
			continue
		}
		for _, e := range mk.Plugins {
			if e.Name != name {
				continue
			}
			src := Source{Kind: "git", Marketplace: mk.Name, MarketplaceURL: mk.URL}
			var rel string
			var obj struct {
				Source string `json:"source"`
				Repo   string `json:"repo"`
				URL    string `json:"url"`
				Path   string `json:"path"`
				Ref    string `json:"ref"`
				SHA    string `json:"sha"`
			}
			switch {
			case json.Unmarshal(e.Source, &rel) == nil:
				if !strings.HasPrefix(rel, "./") && rel != "." {
					rel = "./" + rel
				}
				src.URL, src.Ref, src.Path = mk.URL, mk.Commit, filepath.Clean(rel)
				if mk.Commit == "local" {
					src.Kind, src.URL, src.Ref = "local", filepath.Join(s.Dir, ".marketplaces", mk.Name, filepath.Clean(rel)), ""
					src.Path = ""
				}
			case json.Unmarshal(e.Source, &obj) == nil:
				switch obj.Source {
				case "github":
					src.URL = "https://github.com/" + obj.Repo
				case "url", "git":
					src.URL = obj.URL
				case "git-subdir":
					src.URL, src.Path = obj.URL, obj.Path
				default:
					return Source{}, e, fmt.Errorf("%s@%s: %q sources aren't supported (git, github, url, git-subdir and relative paths are)", name, market, obj.Source)
				}
				src.Ref = orStr(obj.SHA, obj.Ref)
			default:
				return Source{}, e, fmt.Errorf("%s@%s: unreadable source", name, market)
			}
			return src, e, nil
		}
		return Source{}, MarketplaceEntry{}, fmt.Errorf("no plugin %q in marketplace %s", name, market)
	}
	return Source{}, MarketplaceEntry{}, fmt.Errorf("no marketplace %q — /plugin marketplace add <owner/repo>", market)
}
