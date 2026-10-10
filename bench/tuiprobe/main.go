// Command tuiprobe drives a terminal UI in a pseudo-terminal and measures it:
// startup to a stable paint, keystroke-to-echo latency, bytes per frame (the
// payload an SSH link carries, raw and compressed), frames per second while
// output streams, idle CPU and RSS of the process tree, and which rendering
// techniques it uses (alternate screen, synchronized output, full repaints,
// colour depth). A VT emulator keeps the screen, answers the terminal queries
// TUIs send at startup, and gives text snapshots.
//
//	tuiprobe -cols 120 -rows 40 -env TERM=xterm-256color,COLORTERM=truecolor \
//	  -steps 'stable 1500 60000; snap start; keys abcdefghij; idle 30; rss' \
//	  -out run.json -raw run.raw.gz -- ternly
//
// Steps (separated by ';'):
//
//	stable QUIET_MS [MAX_MS]   wait until no output for QUIET_MS (records the time)
//	keys TEXT                  type TEXT one key at a time; per key: echo latency and bytes until quiet
//	type TEXT                  send TEXT at once (\n is Enter, \e is Escape)
//	enter | esc | tab | ctrlc | ctrld
//	stream QUIET_MS MAX_MS     measure output until quiet for QUIET_MS: frames, bytes, fps
//	idle SECS                  CPU % of the process tree over SECS
//	resize COLS ROWS           resize (SIGWINCH), then wait for stable output
//	sleep MS
//	snap NAME                  save the emulated screen as text
//	rss                        resident memory of the process tree (KiB)
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/vt"
	"golang.org/x/sys/unix"
)

// chunk is one read from the terminal: when, and how many bytes.
type chunk struct {
	At time.Duration
	N  int
}

// recorder keeps every byte the program wrote, with timing, and feeds the
// emulator (whose answers to terminal queries go back to the program).
type recorder struct {
	mu     sync.Mutex
	start  time.Time
	buf    bytes.Buffer
	chunks []chunk
	last   time.Time
	notify chan struct{}
	emu    *vt.SafeEmulator
}

func (r *recorder) add(p []byte) {
	r.mu.Lock()
	now := time.Now()
	r.buf.Write(p)
	r.chunks = append(r.chunks, chunk{now.Sub(r.start), len(p)})
	r.last = now
	r.mu.Unlock()
	_, _ = r.emu.Write(p)
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// state returns the bytes so far and the time of the last output.
func (r *recorder) state() (int, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Len(), r.last
}

// Result is the measurement of one run.
type Result struct {
	Cmd     []string          `json:"cmd"`
	Env     []string          `json:"env"`
	Cols    int               `json:"cols"`
	Rows    int               `json:"rows"`
	Steps   []StepResult      `json:"steps"`
	Snaps   map[string]string `json:"snaps,omitempty"`
	Bytes   int               `json:"bytes_total"`
	Gzip    int               `json:"bytes_gzip"`
	Tech    Technique         `json:"technique"`
	Exited  string            `json:"exited,omitempty"`
	Elapsed float64           `json:"elapsed_s"`
}

// StepResult is one step's measurement; unused fields are omitted.
type StepResult struct {
	Step        string    `json:"step"`
	OK          bool      `json:"ok"`
	Note        string    `json:"note,omitempty"`
	MS          float64   `json:"ms,omitempty"`            // stable: time from launch (or step start) to the stable paint
	FirstByteMS float64   `json:"first_byte_ms,omitempty"` // stable at launch: time to the first output byte
	Bytes       int       `json:"bytes,omitempty"`
	EchoMS      []float64 `json:"echo_ms,omitempty"`   // keys: per key, write → first output byte
	KeyBytes    []int     `json:"key_bytes,omitempty"` // keys: per key, bytes until quiet
	Frames      int       `json:"frames,omitempty"`    // stream: output bursts
	FrameBytes  []int     `json:"frame_bytes,omitempty"`
	FPS         float64   `json:"fps,omitempty"`
	GzipBytes   int       `json:"gzip_bytes,omitempty"`
	CPUPercent  float64   `json:"cpu_percent,omitempty"`
	RSSKiB      int       `json:"rss_kib,omitempty"`
}

// Technique is what the output reveals about how the program renders.
type Technique struct {
	AltScreen    bool `json:"alt_screen"`      // DECSET 1049/47/1047
	SyncOutput   int  `json:"sync_updates"`    // DECSET 2026 begin markers
	FullClears   int  `json:"full_clears"`     // ED 2 (erase whole screen)
	TrueColor    int  `json:"sgr_truecolor"`   // SGR 38;2 / 48;2
	Color256     int  `json:"sgr_256"`         // SGR 38;5 / 48;5
	Color16      int  `json:"sgr_16"`          // SGR 30–37, 40–47, 90–97, 100–107
	NonASCII     int  `json:"non_ascii_bytes"` // bytes ≥ 0x80 (UTF-8 glyphs)
	KittyKbd     bool `json:"kitty_keyboard"`  // CSI > … u
	MouseReport  bool `json:"mouse_reporting"` // DECSET 1000/1002/1003/1006
	BracketPaste bool `json:"bracketed_paste"` // DECSET 2004
}

var (
	reCSI     = regexp.MustCompile(`\x1b\[([0-9;:?<>=]*)([A-Za-z])`)
	reDECSET  = regexp.MustCompile(`\x1b\[\?([0-9;]+)h`)
	reSGRpart = regexp.MustCompile(`(?:^|;)(38|48)[;:](2|5)`)
)

// analyse scans the output for rendering techniques.
func analyse(b []byte) Technique {
	var t Technique
	for _, m := range reDECSET.FindAllSubmatch(b, -1) {
		for _, p := range strings.Split(string(m[1]), ";") {
			switch p {
			case "1049", "47", "1047":
				t.AltScreen = true
			case "2026":
				t.SyncOutput++
			case "1000", "1002", "1003", "1006":
				t.MouseReport = true
			case "2004":
				t.BracketPaste = true
			}
		}
	}
	for _, m := range reCSI.FindAllSubmatch(b, -1) {
		params, final := string(m[1]), m[2][0]
		switch {
		case final == 'J' && params == "2":
			t.FullClears++
		case final == 'u' && strings.HasPrefix(params, ">"):
			t.KittyKbd = true
		case final == 'm':
			for _, sm := range reSGRpart.FindAllStringSubmatch(params, -1) {
				if sm[2] == "2" {
					t.TrueColor++
				} else {
					t.Color256++
				}
			}
			stripped := reSGRpart.ReplaceAllString(params, "")
			for _, p := range strings.FieldsFunc(stripped, func(r rune) bool { return r == ';' || r == ':' }) {
				if n, err := strconv.Atoi(p); err == nil && (n >= 30 && n <= 37 || n >= 40 && n <= 47 || n >= 90 && n <= 97 || n >= 100 && n <= 107) {
					t.Color16++
				}
			}
		}
	}
	for _, c := range b {
		if c >= 0x80 {
			t.NonASCII++
		}
	}
	return t
}

// frames splits output chunks into bursts: a gap of at least gap between
// chunks starts a new frame.
func frames(cs []chunk, gap time.Duration) []int {
	var out []int
	var prev time.Duration
	for i, c := range cs {
		if i == 0 || c.At-prev >= gap {
			out = append(out, 0)
		}
		out[len(out)-1] += c.N
		prev = c.At
	}
	return out
}

func gzipLen(b []byte) int {
	var w bytes.Buffer
	z, _ := gzip.NewWriterLevel(&w, gzip.DefaultCompression)
	_, _ = z.Write(b)
	_ = z.Close()
	return w.Len()
}

// tree returns pid and every descendant (from /proc).
func tree(root int) []int {
	ents, _ := os.ReadDir("/proc")
	parent := map[int]int{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) > 2 {
			pp, _ := strconv.Atoi(f[1])
			parent[pid] = pp
		}
	}
	out := []int{root}
	for changed := true; changed; {
		changed = false
		in := map[int]bool{}
		for _, p := range out {
			in[p] = true
		}
		for pid, pp := range parent {
			if in[pp] && !in[pid] {
				out = append(out, pid)
				changed = true
			}
		}
	}
	return out
}

// cpuTicks is utime+stime of the process tree.
func cpuTicks(root int) int64 {
	var sum int64
	for _, pid := range tree(root) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		s := string(b)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(f) > 12 {
			u, _ := strconv.ParseInt(f[11], 10, 64)
			k, _ := strconv.ParseInt(f[12], 10, 64)
			sum += u + k
		}
	}
	return sum
}

func rssKiB(root int) int {
	sum := 0
	for _, pid := range tree(root) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "VmRSS:") {
				f := strings.Fields(l)
				if len(f) > 1 {
					n, _ := strconv.Atoi(f[1])
					sum += n
				}
			}
		}
	}
	return sum
}

const clkTck = 100 // USER_HZ on Linux

// startPTY runs c on a new pseudo-terminal of the given size.
func startPTY(c *exec.Cmd, cols, rows int) (*os.File, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		return nil, err
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, err
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, err
	}
	if err := unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)}); err != nil {
		m.Close()
		s.Close()
		return nil, err
	}
	c.Stdin, c.Stdout, c.Stderr = s, s, s
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := c.Start(); err != nil {
		m.Close()
		s.Close()
		return nil, err
	}
	s.Close()
	return m, nil
}

// unescape turns \n, \r, \e, \t and \xNN in step text into bytes.
func unescape(s string) string {
	r := strings.NewReplacer(`\n`, "\r", `\r`, "\r", `\e`, "\x1b", `\t`, "\t", `\s`, " ")
	s = r.Replace(s)
	re := regexp.MustCompile(`\\x([0-9a-fA-F]{2})`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		v, _ := strconv.ParseUint(m[2:], 16, 8)
		return string([]byte{byte(v)})
	})
}

type probe struct {
	rec  *recorder
	m    *os.File
	cmd  *exec.Cmd
	res  *Result
	done chan struct{}
	// launched: the next stable step measures from process start (startup)
	launched bool
}

// waitQuiet waits until no output for quiet (after at least one byte since
// from, if needFirst), up to max. It returns the time of the last output.
func (p *probe) waitQuiet(from int, quiet, max time.Duration, needFirst bool) (time.Time, bool) {
	deadline := time.Now().Add(max)
	for {
		n, last := p.rec.state()
		if (!needFirst || n > from) && !last.IsZero() && time.Since(last) >= quiet {
			return last, true
		}
		if time.Now().After(deadline) {
			return last, false
		}
		select {
		case <-p.done:
			return last, n > from
		case <-p.rec.notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (p *probe) send(s string) { _, _ = p.m.Write([]byte(s)) }

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (p *probe) run(step string) StepResult {
	f := strings.Fields(step)
	sr := StepResult{Step: step, OK: true}
	argInt := func(i, def int) int {
		if len(f) > i {
			if v, err := strconv.Atoi(f[i]); err == nil {
				return v
			}
		}
		return def
	}
	rest := ""
	if len(f) > 1 {
		rest = strings.TrimSpace(strings.TrimPrefix(step, f[0]))
	}
	switch f[0] {
	case "stable":
		quiet, max := time.Duration(argInt(1, 1500))*time.Millisecond, time.Duration(argInt(2, 60000))*time.Millisecond
		n0, _ := p.rec.state()
		t0 := time.Now()
		atLaunch := !p.launched
		p.launched = true
		if atLaunch {
			n0 = 0
		}
		last, ok := p.waitQuiet(n0, quiet, max, true)
		sr.OK = ok
		p.rec.mu.Lock()
		var first time.Duration = -1
		if len(p.rec.chunks) > 0 {
			first = p.rec.chunks[0].At
		}
		p.rec.mu.Unlock()
		if atLaunch && first >= 0 { // startup: from process start
			sr.MS = ms(last.Sub(p.rec.start))
			sr.FirstByteMS = ms(first)
		} else if !last.IsZero() && last.After(t0) {
			sr.MS = ms(last.Sub(t0))
		}
		n1, _ := p.rec.state()
		sr.Bytes = n1 - n0
	case "keys":
		for _, k := range unescape(rest) {
			n0, _ := p.rec.state()
			t0 := time.Now()
			p.send(string(k))
			// first byte after the write
			first := time.Duration(-1)
			deadline := t0.Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if n, _ := p.rec.state(); n > n0 {
					first = time.Since(t0)
					break
				}
				select {
				case <-p.rec.notify:
				case <-time.After(time.Millisecond):
				}
			}
			p.waitQuiet(n0, 150*time.Millisecond, 3*time.Second, false)
			n1, _ := p.rec.state()
			if first < 0 {
				sr.EchoMS = append(sr.EchoMS, -1)
			} else {
				sr.EchoMS = append(sr.EchoMS, ms(first))
			}
			sr.KeyBytes = append(sr.KeyBytes, n1-n0)
		}
	case "type":
		p.send(unescape(rest))
	case "enter":
		p.send("\r")
	case "esc":
		p.send("\x1b")
	case "tab":
		p.send("\t")
	case "ctrlc":
		p.send("\x03")
	case "ctrld":
		p.send("\x04")
	case "sleep":
		time.Sleep(time.Duration(argInt(1, 500)) * time.Millisecond)
	case "stream":
		quiet, max := time.Duration(argInt(1, 6000))*time.Millisecond, time.Duration(argInt(2, 300000))*time.Millisecond
		p.rec.mu.Lock()
		i0, n0 := len(p.rec.chunks), p.rec.buf.Len()
		p.rec.mu.Unlock()
		t0 := time.Now()
		last, ok := p.waitQuiet(n0, quiet, max, true)
		sr.OK = ok
		p.rec.mu.Lock()
		cs := append([]chunk(nil), p.rec.chunks[i0:]...)
		seg := append([]byte(nil), p.rec.buf.Bytes()[n0:]...)
		p.rec.mu.Unlock()
		fr := frames(cs, 4*time.Millisecond)
		sr.Frames, sr.FrameBytes, sr.Bytes = len(fr), fr, len(seg)
		sr.GzipBytes = gzipLen(seg)
		if len(cs) > 1 {
			span := cs[len(cs)-1].At - cs[0].At
			if span > 0 {
				sr.FPS = float64(len(fr)) / span.Seconds()
			}
		}
		if !last.IsZero() {
			sr.MS = ms(last.Sub(t0))
		}
	case "idle":
		secs := argInt(1, 30)
		a := cpuTicks(p.cmd.Process.Pid)
		t0 := time.Now()
		time.Sleep(time.Duration(secs) * time.Second)
		b := cpuTicks(p.cmd.Process.Pid)
		sr.CPUPercent = float64(b-a) / clkTck / time.Since(t0).Seconds() * 100
	case "resize":
		cols, rows := argInt(1, 80), argInt(2, 24)
		n0, _ := p.rec.state()
		t0 := time.Now()
		_ = unix.IoctlSetWinsize(int(p.m.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
		p.rec.emu.Resize(cols, rows)
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGWINCH)
		last, ok := p.waitQuiet(n0, 800*time.Millisecond, 10*time.Second, true)
		sr.OK = ok
		n1, _ := p.rec.state()
		sr.Bytes = n1 - n0
		if !last.IsZero() && last.After(t0) {
			sr.MS = ms(last.Sub(t0))
		}
		p.res.Cols, p.res.Rows = cols, rows
	case "snap":
		if p.res.Snaps == nil {
			p.res.Snaps = map[string]string{}
		}
		p.res.Snaps[rest] = p.rec.emu.String()
	case "rss":
		sr.RSSKiB = rssKiB(p.cmd.Process.Pid)
	default:
		sr.OK, sr.Note = false, "unknown step"
	}
	return sr
}

func main() {
	cols := flag.Int("cols", 120, "terminal columns")
	rows := flag.Int("rows", 40, "terminal rows")
	envs := flag.String("env", "", "comma-separated KEY=VALUE added to the environment (KEY= removes KEY)")
	clean := flag.Bool("clean-env", false, "start from an empty environment (PATH, HOME, USER kept unless overridden)")
	steps := flag.String("steps", "stable 1500 60000; snap start", "the steps, ';'-separated")
	out := flag.String("out", "", "result JSON (default: stdout)")
	raw := flag.String("raw", "", "gzipped raw output with a chunk timing index (optional)")
	dir := flag.String("dir", "", "working directory")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: tuiprobe [flags] -- command [args]")
		os.Exit(2)
	}
	res, err := probeRun(flag.Args(), *cols, *rows, *envs, *clean, *dir, *steps, *raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tuiprobe:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if *out == "" {
		os.Stdout.Write(append(b, '\n'))
		return
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "tuiprobe:", err)
		os.Exit(1)
	}
}

func buildEnv(spec string, clean bool) []string {
	base := os.Environ()
	if clean {
		base = nil
		for _, k := range []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL"} {
			if v, ok := os.LookupEnv(k); ok {
				base = append(base, k+"="+v)
			}
		}
	}
	m := map[string]string{}
	var order []string
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = v
	}
	if spec != "" {
		for _, kv := range strings.Split(spec, ",") {
			k, v, _ := strings.Cut(kv, "=")
			if _, ok := m[k]; !ok {
				order = append(order, k)
			}
			if v == "" && !strings.HasSuffix(kv, "=") {
				continue
			}
			if v == "" {
				delete(m, k)
				continue
			}
			m[k] = v
		}
	}
	var env []string
	for _, k := range order {
		if v, ok := m[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func probeRun(argv []string, cols, rows int, envSpec string, clean bool, dir, steps, rawPath string) (*Result, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = buildEnv(envSpec, clean)
	c.Dir = dir
	emu := vt.NewSafeEmulator(cols, rows)
	rec := &recorder{notify: make(chan struct{}, 1), emu: emu}
	rec.start = time.Now()
	m, err := startPTY(c, cols, rows)
	if err != nil {
		return nil, err
	}
	p := &probe{rec: rec, m: m, cmd: c, done: make(chan struct{})}
	p.res = &Result{Cmd: argv, Env: strings.Split(envSpec, ","), Cols: cols, Rows: rows}
	go func() { // the emulator's answers to terminal queries go back to the program
		buf := make([]byte, 4096)
		for {
			n, err := emu.Read(buf)
			if n > 0 {
				_, _ = m.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := m.Read(buf)
			if n > 0 {
				rec.add(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	waitErr := make(chan error, 1)
	go func() { waitErr <- c.Wait(); close(p.done) }()
	for _, s := range strings.Split(steps, ";") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		select {
		case <-p.done:
			p.res.Steps = append(p.res.Steps, StepResult{Step: s, OK: false, Note: "the program had exited"})
			continue
		default:
		}
		p.res.Steps = append(p.res.Steps, p.run(s))
	}
	// stop the program: its whole process group, gently then firmly
	_ = syscall.Kill(-c.Process.Pid, syscall.SIGTERM)
	select {
	case err := <-waitErr:
		p.res.Exited = fmt.Sprint(err)
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		p.res.Exited = fmt.Sprint(<-waitErr)
	}
	m.Close()
	_ = emu.Close()
	rec.mu.Lock()
	all := append([]byte(nil), rec.buf.Bytes()...)
	cs := append([]chunk(nil), rec.chunks...)
	rec.mu.Unlock()
	p.res.Bytes, p.res.Gzip = len(all), gzipLen(all)
	p.res.Tech = analyse(all)
	p.res.Elapsed = time.Since(rec.start).Seconds()
	if rawPath != "" {
		if err := writeRaw(rawPath, all, cs); err != nil {
			return p.res, err
		}
	}
	return p.res, nil
}

// writeRaw stores the output and a timing index: gzip of
// [u32 count][count × (i64 ns, u32 len)][bytes].
func writeRaw(path string, all []byte, cs []chunk) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	_ = binary.Write(z, binary.LittleEndian, uint32(len(cs)))
	for _, c := range cs {
		_ = binary.Write(z, binary.LittleEndian, int64(c.At))
		_ = binary.Write(z, binary.LittleEndian, uint32(c.N))
	}
	_, _ = z.Write(all)
	if err := z.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readRaw is writeRaw's inverse (used by tests and analysis).
func readRaw(path string) ([]byte, []chunk, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, err
	}
	var n uint32
	if err := binary.Read(z, binary.LittleEndian, &n); err != nil {
		return nil, nil, err
	}
	cs := make([]chunk, n)
	for i := range cs {
		var at int64
		var l uint32
		if err := binary.Read(z, binary.LittleEndian, &at); err != nil {
			return nil, nil, err
		}
		if err := binary.Read(z, binary.LittleEndian, &l); err != nil {
			return nil, nil, err
		}
		cs[i] = chunk{time.Duration(at), int(l)}
	}
	b, err := io.ReadAll(z)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, nil, err
	}
	return b, cs, nil
}

// median of xs (0 for none).
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}
