package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/rajasatyajit/ternly/internal/mcphttp"
)

// RemoteServer is a remote MCP server (Streamable HTTP; ADR 014).
type RemoteServer struct {
	Name, URL string
	Tools     int
	c         *mcphttp.Client
}

// RemoteAuth supplies tokens and answers challenges (mcpauth.Flow); nil for
// servers without OAuth.
type RemoteAuth interface {
	Token(ctx context.Context) (string, error)
	Authorize(ctx context.Context, ch mcphttp.Challenge) error
}

// StartRemote connects to a remote server through httpc (its network grant),
// lists its tools and registers them as mcp__<name>__<tool>.
func StartRemote(ctx context.Context, reg *Registry, name, url string, headers map[string]string, httpc *http.Client, auth RemoteAuth, version string) (*RemoteServer, error) {
	c := &mcphttp.Client{URL: url, HTTP: httpc, Headers: headers, Name: "ternly", Version: version}
	if auth != nil {
		c.Token, c.Authorize = auth.Token, auth.Authorize
	}
	s := &RemoteServer{Name: name, URL: url, c: c}
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	var specs []json.RawMessage
	cursor := ""
	for pages := 0; pages < maxToolPages; pages++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.Call(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		var tl struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &tl); err != nil {
			return nil, err
		}
		specs = append(specs, tl.Tools...)
		if cursor = tl.NextCursor; cursor == "" || len(specs) >= maxServerTools {
			break
		}
	}
	list, _ := json.Marshal(map[string]any{"tools": specs})
	parsed, _, err := parseToolList(name, list)
	if err != nil {
		return nil, err
	}
	for i, sp := range parsed {
		var raw struct {
			InputSchema json.RawMessage `json:"inputSchema"`
		}
		_ = json.Unmarshal(specs[i], &raw)
		c.SetToolHeaders(sp.Name, raw.InputSchema)
		reg.Add(MCPTool(s, ToolName("mcp", name, sp.Name), sp))
	}
	s.Tools = len(parsed)
	return s, nil
}

// CallTool implements MCPCaller.
func (s *RemoteServer) CallTool(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	var a any = map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
	}
	res, err := s.c.Call(ctx, "tools/call", map[string]any{"name": tool, "arguments": a})
	if err != nil {
		return "", err
	}
	var rt struct {
		ResultType string `json:"resultType"`
	}
	if json.Unmarshal(res, &rt) == nil && rt.ResultType == "input_required" {
		return "", errors.New("the server asked for more input (elicitation or sampling), which ternly doesn't support")
	}
	return parseCallResult(res)
}

// Era is the protocol generation the server speaks.
func (s *RemoteServer) Era() string { return string(s.c.Era()) }

// Close ends the server's session, if it keeps one.
func (s *RemoteServer) Close() { s.c.Close() }
