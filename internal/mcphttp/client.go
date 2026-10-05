// Package mcphttp is an MCP client for remote servers over Streamable HTTP
// (ADR 014). It speaks the current revision (2026-07-28: stateless POSTs
// carrying the protocol version in _meta and headers) and falls back to the
// 2025 revisions (initialize, Mcp-Session-Id) when a server answers a modern
// request with anything but a modern error, as the spec prescribes. The
// deprecated HTTP+SSE transport (2024-11-05) is not supported.
package mcphttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/rajasatyajit/ternly/internal/sse"
)

// Protocol versions.
const (
	Modern = "2026-07-28"
	Legacy = "2025-11-25"
)

var legacyVersions = map[string]bool{"2025-11-25": true, "2025-06-18": true, "2025-03-26": true}

// Era is which protocol generation a server speaks.
type Era string

const (
	EraModern Era = "2026-07-28"
	EraLegacy Era = "2025 (sessions)"
)

// Challenge is an authorization challenge from the server (401, or 403
// insufficient_scope), parsed from WWW-Authenticate.
type Challenge struct {
	Status           int
	ResourceMetadata string // resource_metadata
	Scope            string
	Error            string
}

// Client talks to one remote MCP server.
type Client struct {
	URL     string
	HTTP    *http.Client      // the server's network grant (netguard)
	Headers map[string]string // static headers (API-key servers)
	Name    string            // clientInfo name
	Version string            // clientInfo version
	// Token returns the bearer token to send ("" = none).
	Token func(ctx context.Context) (string, error)
	// Authorize handles a challenge (OAuth: discovery, login, refresh, step-up).
	// After it returns nil the request is retried once.
	Authorize func(ctx context.Context, ch Challenge) error

	mu       sync.Mutex
	era      Era
	version  string
	session  string
	nextID   int64
	headerOf map[string]map[string]string // tool → argument → Mcp-Param name (x-mcp-header)
}

// Error is a JSON-RPC error from the server.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Message) }

// HTTPError is a non-2xx status with no JSON-RPC error in the body.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, strings.TrimSpace(e.Body))
}

// Era reports the negotiated protocol generation ("" before Connect).
func (c *Client) Era() Era { c.mu.Lock(); defer c.mu.Unlock(); return c.era }

// Connect negotiates the protocol: server/discover (modern); on a 400, 404
// or 405 that isn't a modern error, initialize (legacy).
func (c *Client) Connect(ctx context.Context) error {
	res, err := c.send(ctx, EraModern, Modern, "", "server/discover", map[string]any{}, true)
	if err == nil {
		var d struct {
			SupportedVersions []string `json:"supportedVersions"`
		}
		_ = json.Unmarshal(res, &d)
		for _, v := range d.SupportedVersions {
			if v == Modern {
				c.set(EraModern, Modern, "")
				return nil
			}
		}
		return fmt.Errorf("server supports %v; ternly speaks %s and the 2025 revisions", d.SupportedVersions, Modern)
	}
	var rpc *Error
	var he *HTTPError
	switch {
	case errors.As(err, &rpc) && rpc.Code == -32022: // a modern error: retry with a version it advertises
		var d struct {
			Supported []string `json:"supported"`
		}
		_ = json.Unmarshal(rpc.Data, &d)
		ok := false
		for _, v := range d.Supported {
			ok = ok || legacyVersions[v]
		}
		if !ok {
			return fmt.Errorf("server supports %v; ternly speaks %s and the 2025 revisions", d.Supported, Modern)
		}
	case errors.As(err, &rpc), errors.As(err, &he) && (he.Status == 400 || he.Status == 404 || he.Status == 405):
		// not a modern server: the 2025 handshake
	default:
		return err
	}
	return c.initialize(ctx)
}

// initialize opens a 2025-revision session.
func (c *Client) initialize(ctx context.Context) error {
	params := map[string]any{"protocolVersion": Legacy, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": c.Name, "version": c.Version}}
	res, sess, err := c.post(ctx, EraLegacy, Legacy, "", "initialize", params, true)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(res, &r)
	if !legacyVersions[r.ProtocolVersion] {
		return fmt.Errorf("server negotiated protocol %q, which ternly doesn't speak", r.ProtocolVersion)
	}
	c.set(EraLegacy, r.ProtocolVersion, sess)
	_, _, err = c.post(ctx, EraLegacy, r.ProtocolVersion, sess, "notifications/initialized", nil, false)
	return err
}

func (c *Client) set(e Era, v, s string) {
	c.mu.Lock()
	c.era, c.version, c.session = e, v, s
	c.mu.Unlock()
}

// Call sends a request and returns its result. A legacy session the server
// has forgotten (404) is re-initialized once.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	era, v, s := c.era, c.version, c.session
	c.mu.Unlock()
	if era == "" {
		if err := c.Connect(ctx); err != nil {
			return nil, err
		}
		return c.Call(ctx, method, params)
	}
	res, err := c.send(ctx, era, v, s, method, params, true)
	var he *HTTPError
	if era == EraLegacy && s != "" && errors.As(err, &he) && he.Status == 404 {
		if err := c.initialize(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		v, s = c.version, c.session
		c.mu.Unlock()
		return c.send(ctx, era, v, s, method, params, true)
	}
	return res, err
}

// send posts with one authorization retry.
func (c *Client) send(ctx context.Context, era Era, version, session, method string, params any, request bool) (json.RawMessage, error) {
	res, _, err := c.post(ctx, era, version, session, method, params, request)
	var ch *challengeError
	if errors.As(err, &ch) && c.Authorize != nil {
		if aerr := c.Authorize(ctx, ch.Challenge); aerr != nil {
			return nil, aerr
		}
		res, _, err = c.post(ctx, era, version, session, method, params, request)
	}
	return res, err
}

type challengeError struct{ Challenge }

func (e *challengeError) Error() string {
	return fmt.Sprintf("HTTP %d: authorization required%s", e.Status, map[bool]string{true: " (" + e.Challenge.Error + ")", false: ""}[e.Challenge.Error != ""])
}

// post sends one JSON-RPC message; for a request it returns the result and
// any session ID the response assigned.
func (c *Client) post(ctx context.Context, era Era, version, session, method string, params any, request bool) (json.RawMessage, string, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	var id int64
	if request {
		c.mu.Lock()
		c.nextID++
		id = c.nextID
		c.mu.Unlock()
		msg["id"] = id
	}
	p := map[string]any{}
	if params != nil {
		b, _ := json.Marshal(params)
		_ = json.Unmarshal(b, &p)
	}
	if era == EraModern {
		meta, _ := p["_meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		meta["io.modelcontextprotocol/protocolVersion"] = version
		meta["io.modelcontextprotocol/clientCapabilities"] = map[string]any{}
		meta["io.modelcontextprotocol/clientInfo"] = map[string]any{"name": c.Name, "version": c.Version}
		p["_meta"] = meta
	}
	if params != nil || era == EraModern {
		msg["params"] = p
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", version)
	if era == EraModern {
		req.Header.Set("Mcp-Method", method)
		if name, _ := p["name"].(string); name != "" && (method == "tools/call" || method == "prompts/get") {
			req.Header.Set("Mcp-Name", headerValue(name))
		} else if uri, _ := p["uri"].(string); uri != "" && method == "resources/read" {
			req.Header.Set("Mcp-Name", headerValue(uri))
		}
		if method == "tools/call" {
			c.paramHeaders(req, p)
		}
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	if c.Token != nil {
		tok, err := c.Token(ctx)
		if err != nil {
			return nil, "", err
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	sess := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		ch := ParseChallenge(resp.Header.Get("WWW-Authenticate"))
		ch.Status = resp.StatusCode
		if resp.StatusCode == 401 || ch.Error == "insufficient_scope" {
			return nil, sess, &challengeError{ch}
		}
	}
	if !request {
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, sess, &HTTPError{resp.StatusCode, string(b)}
		}
		return nil, sess, nil
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "text/event-stream" && resp.StatusCode/100 == 2 {
		res, err := readStream(resp.Body, id)
		return res, sess, err
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	res, rpcErr, ok := decodeResponse(b, id)
	switch {
	case ok && rpcErr != nil:
		return nil, sess, rpcErr
	case ok && resp.StatusCode/100 == 2:
		return res, sess, nil
	case resp.StatusCode/100 != 2:
		return nil, sess, &HTTPError{resp.StatusCode, string(b[:min(len(b), 2048)])}
	}
	return nil, sess, fmt.Errorf("unreadable response: %s", string(b[:min(len(b), 300)]))
}

// decodeResponse reads a JSON-RPC response for id. A response without
// jsonrpc or id is accepted as the answer to the one request in flight
// (Context7 answers so).
func decodeResponse(b []byte, id int64) (json.RawMessage, *Error, bool) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if json.Unmarshal(b, &m) != nil || m.Method != "" {
		return nil, nil, false
	}
	if len(m.ID) > 0 && string(m.ID) != "null" && string(m.ID) != strconv.FormatInt(id, 10) {
		if m.Error == nil { // an error with a server-chosen id is still this request's error
			return nil, nil, false
		}
	}
	if m.Error != nil {
		return nil, m.Error, true
	}
	if m.Result == nil {
		return nil, nil, false
	}
	return m.Result, nil, true
}

// readStream reads an SSE response until the response to id arrives;
// notifications on the way are skipped.
func readStream(r io.Reader, id int64) (json.RawMessage, error) {
	var res json.RawMessage
	var rerr error
	found := false
	err := sse.Read(r, func(_, data string) bool {
		if v, e, ok := decodeResponse([]byte(data), id); ok {
			res, rerr, found = v, nil, true
			if e != nil {
				rerr = e
			}
			return false
		}
		return true
	})
	switch {
	case found:
		return res, rerr
	case err != nil:
		return nil, err
	}
	return nil, errors.New("the server closed the stream without a response")
}

// headerValue encodes a header value the spec's way: as is when it is
// printable ASCII, else =?base64?…?=.
func headerValue(s string) string {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
		}
	}
	return s
}

// SetToolHeaders records which tool arguments a server wants mirrored into
// Mcp-Param-* headers (the x-mcp-header schema annotation).
func (c *Client) SetToolHeaders(tool string, schema json.RawMessage) {
	var s struct {
		Properties map[string]struct {
			Header string `json:"x-mcp-header"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return
	}
	for arg, p := range s.Properties {
		if p.Header == "" {
			continue
		}
		c.mu.Lock()
		if c.headerOf == nil {
			c.headerOf = map[string]map[string]string{}
		}
		if c.headerOf[tool] == nil {
			c.headerOf[tool] = map[string]string{}
		}
		c.headerOf[tool][arg] = p.Header
		c.mu.Unlock()
	}
}

func (c *Client) paramHeaders(req *http.Request, p map[string]any) {
	name, _ := p["name"].(string)
	args, _ := p["arguments"].(map[string]any)
	c.mu.Lock()
	hs := c.headerOf[name]
	c.mu.Unlock()
	for arg, h := range hs {
		switch v := args[arg].(type) {
		case string:
			req.Header.Set("Mcp-Param-"+h, headerValue(v))
		case float64, bool:
			req.Header.Set("Mcp-Param-"+h, fmt.Sprint(v))
		}
	}
}

// Close ends a legacy session (DELETE); modern servers keep no session.
func (c *Client) Close() {
	c.mu.Lock()
	era, v, s := c.era, c.version, c.session
	c.mu.Unlock()
	if era != EraLegacy || s == "" {
		return
	}
	req, err := http.NewRequest("DELETE", c.URL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", s)
	req.Header.Set("MCP-Protocol-Version", v)
	if resp, err := c.HTTP.Do(req); err == nil {
		resp.Body.Close()
	}
}

// ParseChallenge reads a Bearer WWW-Authenticate header.
func ParseChallenge(h string) Challenge {
	var ch Challenge
	_, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(strings.TrimSpace(h[:len(h)-len(rest)]), "bearer") {
		return ch
	}
	for _, part := range splitParams(rest) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "resource_metadata":
			ch.ResourceMetadata = v
		case "scope":
			ch.Scope = v
		case "error":
			ch.Error = v
		}
	}
	return ch
}

// splitParams splits auth-params on commas outside quotes.
func splitParams(s string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			b.WriteRune(r)
		case r == ',' && !quoted:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
