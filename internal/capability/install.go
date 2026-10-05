package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rajasatyajit/ternly/internal/plugins"
)

// Prepare fetches a candidate as a pending plugin, for the same review and
// approval as /plugin add. Marketplace plugins come through their
// marketplace (fetched from its listed repository, so trust is computed from
// it), Gemini extensions from their repository, and registry or npm MCP
// servers as a generated plugin whose command pins the package version.
func Prepare(ctx context.Context, st *plugins.Store, e Entry) (*plugins.Pending, error) {
	switch e.Kind {
	case "plugin":
		spec, repo, _ := strings.Cut(e.Install, " ")
		name, market, _ := strings.Cut(spec, "@")
		have := false
		if all, err := st.Marketplaces(); err == nil {
			for _, m := range all {
				have = have || m.Name == market
			}
		}
		if !have {
			if repo == "" {
				return nil, fmt.Errorf("marketplace %s isn't known", market)
			}
			if _, err := st.AddMarketplace(ctx, repo); err != nil {
				return nil, err
			}
		}
		src, _, err := st.Resolve(name, market)
		if err != nil {
			return nil, err
		}
		return st.Fetch(ctx, src)
	case "extension":
		return st.Fetch(ctx, plugins.Source{Kind: "git", URL: e.Install})
	case "mcp", "npm":
		dir, err := generate(st.Dir, e)
		if err != nil {
			return nil, err
		}
		p, err := st.Fetch(ctx, plugins.Source{Kind: "local", URL: dir})
		os.RemoveAll(dir)
		if err != nil {
			return nil, err
		}
		p.Source = plugins.Source{Kind: "generated", URL: e.Source + ":" + e.Name}
		switch {
		case e.Verified:
			p.Trust = plugins.Trust{Level: "listed", Why: "listed in the MCP registry, whose publisher namespace " + e.Publisher + " is ownership-verified; runs " + e.Runs}
		default:
			p.Trust = plugins.Trust{Level: "unverified", Why: "an npm package published by " + orStr(e.Publisher, "unknown") + "; runs " + e.Runs}
		}
		return p, nil
	}
	return nil, fmt.Errorf("%s entries can't be installed", e.Kind)
}

var reSafeName = regexp.MustCompile(`[^a-z0-9-]+`)

// generate writes a minimal Claude-format plugin that starts the package as
// an MCP server: npx -y pkg@version, or uvx pkg==version.
func generate(base string, e Entry) (string, error) {
	kind, pkg, ok := strings.Cut(e.Install, ":")
	if !ok {
		return "", fmt.Errorf("no package to run for %s", e.Name)
	}
	var cmd string
	var args []string
	switch kind {
	case "npm":
		cmd, args = "npx", []string{"-y", pkg}
	case "pypi":
		cmd, args = "uvx", []string{pkg}
	default:
		return "", fmt.Errorf("%s packages aren't supported", kind)
	}
	short := e.Name[strings.LastIndexAny(e.Name, "/")+1:]
	name := strings.Trim(reSafeName.ReplaceAllString(strings.ToLower(short), "-"), "-")
	if name == "" || !regexp.MustCompile(`^[a-z0-9]`).MatchString(name) {
		name = "mcp-" + name
	}
	if len(name) > 48 {
		name = name[:48]
	}
	dir := filepath.Join(base, ".generated", name)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o700); err != nil {
		return "", err
	}
	manifest, _ := json.MarshalIndent(map[string]any{"name": name, "version": e.Version, "description": e.Description, "repository": e.URL}, "", "  ")
	env := map[string]string{}
	for _, s := range e.Secrets {
		env[s] = "${" + s + "}" // supplied with /plugin env (plugins never inherit your environment)
	}
	server := map[string]any{"command": cmd, "args": args}
	if len(env) > 0 {
		server["env"] = env
	}
	mcp, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{name: server}}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), manifest, 0o600); err != nil {
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, ".mcp.json"), mcp, 0o600)
}
