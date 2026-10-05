package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rajasatyajit/ternly/internal/llm"
)

// MCPConfig uses the same shape as Claude Code / Claude Desktop:
// {"mcpServers": {"name": {"command": "...", "args": [...], "env": {...}, "trusted": false}}}
type MCPConfig struct {
	Servers map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Trusted bool              `json:"trusted"`
	} `json:"mcpServers"`
}

type MCPServer struct {
	Name    string
	cmd     *exec.Cmd
	in      io.WriteCloser
	mu      sync.Mutex
	pending map[int64]chan rpcResp
	nextID  int64
	done    chan struct{}
	Tools   int
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var reToolName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// LoadMCP starts every configured server concurrently and registers its tools.
// Servers that fail are reported, never fatal.
func LoadMCP(ctx context.Context, reg *Registry, paths []string) ([]*MCPServer, []string) {
	var cfg MCPConfig
	cfg.Servers = map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Trusted bool              `json:"trusted"`
	}{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var c MCPConfig
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, []string{fmt.Sprintf("%s: %v", p, err)}
		}
		for k, v := range c.Servers {
			cfg.Servers[k] = v
		}
	}
	var (
		mu   sync.Mutex
		srvs []*MCPServer
		warn []string
		wg   sync.WaitGroup
	)
	for name, sc := range cfg.Servers {
		wg.Add(1)
		go func(name string, command string, args []string, env map[string]string, trusted bool) {
			defer wg.Done()
			s, specs, err := startMCP(ctx, name, command, args, env)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warn = append(warn, fmt.Sprintf("mcp %s: %v", name, err))
				return
			}
			if trusted {
				reg.Policy.mu.Lock()
				reg.Policy.Trusted[name] = true
				reg.Policy.mu.Unlock()
			}
			for _, sp := range specs {
				sp := sp
				orig := sp.Name
				sp.Name = "mcp__" + reToolName.ReplaceAllString(name, "_") + "__" + reToolName.ReplaceAllString(orig, "_")
				if len(sp.Name) > 64 {
					sp.Name = sp.Name[:64]
				}
				reg.Add(&Tool{Spec: sp, Kind: External,
					Summary: func(a json.RawMessage) string { return orig + " " + Cap(string(a), 200) },
					Run: func(ctx context.Context, a json.RawMessage) (string, error) {
						return s.call(ctx, orig, a)
					}})
			}
			s.Tools = len(specs)
			srvs = append(srvs, s)
		}(name, sc.Command, sc.Args, sc.Env, sc.Trusted)
	}
	wg.Wait()
	return srvs, warn
}

func startMCP(ctx context.Context, name, command string, args []string, env map[string]string) (*MCPServer, []llm.ToolSpec, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+os.ExpandEnv(v))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return StartMCPCmd(ctx, name, cmd)
}

// StartMCPCmd starts an MCP server from a prepared (e.g. confined) command
// and lists its tools; it fails unless initialize and tools/list succeed.
func StartMCPCmd(ctx context.Context, name string, cmd *exec.Cmd) (*MCPServer, []llm.ToolSpec, error) {
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	s := &MCPServer{Name: name, cmd: cmd, in: in, pending: map[int64]chan rpcResp{}, done: make(chan struct{})}
	go s.read(out)

	ictx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := s.rpc(ictx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ternly", "version": "0.1"},
	}); err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("initialize: %w", err)
	}
	_ = s.notify("notifications/initialized")
	var specs []llm.ToolSpec
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := s.rpc(ictx, "tools/list", params)
		if err != nil {
			s.Close()
			return nil, nil, fmt.Errorf("tools/list: %w", err)
		}
		var tl struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &tl); err != nil {
			s.Close()
			return nil, nil, err
		}
		for _, t := range tl.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			specs = append(specs, llm.ToolSpec{Name: t.Name, Description: Cap(t.Description, 1024), Schema: schema})
		}
		if cursor = tl.NextCursor; cursor == "" {
			break
		}
	}
	return s, specs, nil
}

// MCPTool wraps one of a server's tools as a registry tool named name.
func MCPTool(s *MCPServer, name string, sp llm.ToolSpec) *Tool {
	orig := sp.Name
	sp.Name = name
	return &Tool{Spec: sp, Kind: External,
		Summary: func(a json.RawMessage) string { return orig + " " + Cap(string(a), 200) },
		Run:     func(ctx context.Context, a json.RawMessage) (string, error) { return s.call(ctx, orig, a) }}
}

// ToolName makes a valid tool name from parts (characters outside
// [A-Za-z0-9_-] become _, at most 64 long).
func ToolName(parts ...string) string {
	for i, p := range parts {
		parts[i] = reToolName.ReplaceAllString(p, "_")
	}
	n := strings.Join(parts, "__")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

func (s *MCPServer) read(r io.Reader) {
	defer close(s.done)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	for sc.Scan() {
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
			continue // notifications / logs
		}
		if m.Method != "" { // server→client request (sampling, roots…): politely decline
			_ = s.send(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "error": map[string]any{"code": -32601, "message": "not supported"}})
			continue
		}
		s.mu.Lock()
		ch := s.pending[*m.ID]
		delete(s.pending, *m.ID)
		s.mu.Unlock()
		if ch != nil {
			ch <- rpcResp{Result: m.Result, Error: m.Error}
		}
	}
}

func (s *MCPServer) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.in.Write(append(b, '\n'))
	return err
}

func (s *MCPServer) notify(method string) error {
	return s.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

func (s *MCPServer) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ch := make(chan rpcResp, 1)
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.pending[id] = ch
	s.mu.Unlock()
	if err := s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, fmt.Errorf("%d: %s", r.Error.Code, r.Error.Message)
		}
		return r.Result, nil
	case <-s.done:
		return nil, errors.New("server exited")
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (s *MCPServer) call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := s.rpc(cctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var r struct {
		Content []struct {
			Type, Text string
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return Cap(string(res), maxOutBytes), nil
	}
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
			sb.WriteByte('\n')
		} else {
			fmt.Fprintf(&sb, "[%s content omitted]\n", c.Type)
		}
	}
	out := Cap(sb.String(), maxOutBytes)
	if r.IsError {
		return out, errors.New("tool reported an error")
	}
	return out, nil
}

func (s *MCPServer) Close() {
	_ = s.in.Close()
	if s.cmd.Process != nil {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		go func() {
			time.Sleep(2 * time.Second)
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		}()
		_ = s.cmd.Wait()
	}
}
