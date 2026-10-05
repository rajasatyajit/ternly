package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/rajasatyajit/ternly/internal/logstore"
)

// Session logs are read back after crashes, disk trouble or hand edits.
// Run: go test -fuzz FuzzLogRead ./internal/session (and FuzzReplay).

// FuzzLogRead: any bytes. The reader never panics and returns only records
// whose checksum matched.
func FuzzLogRead(f *testing.F) {
	f.Add(line(`{"t":"turn","prompt":"hi"}`) + "garbage\n" + line(`{"t":"msg"}`))
	f.Add("0000000a \n")
	f.Add(line(`{}`)[:5])
	f.Fuzz(func(t *testing.T, s string) {
		recs, off, err := logstore.ReadAll(bytes.NewReader([]byte(s)))
		if err != nil {
			return
		}
		if off > int64(len(s)) {
			t.Fatalf("offset %d past the end (%d)", off, len(s))
		}
		_ = decodeRecords(recs)
	})
}

// FuzzReplay: well-formed log lines carrying arbitrary records (one JSON
// payload per input line). Opening the session never panics, and the
// repaired history is valid for every provider: each tool call has a result
// and each result answers an earlier call.
func FuzzReplay(f *testing.F) {
	f.Add(`{"t":"turn","prompt":"do it","ts":1}
{"t":"msg","msg":{"Role":"user","Content":"do it"}}
{"t":"msg","msg":{"Role":"assistant","ToolCalls":[{"ID":"t1","Name":"bash","Args":"{}"}]}}`)
	f.Add(`{"t":"msg","msg":{"Role":"tool","ToolCallID":"nope","Content":"x"}}
{"t":"rewind","n":99,"mode":"both"}
{"t":"compact","cut":-3,"text":"s"}
{"t":"snapshot","state":{"History":[{"Role":"assistant","ToolCalls":[{"ID":"a"},{"ID":"a"}]}],"Turns":[{"Hist":1000}]}}`)
	f.Add(`{"t":"compact","cut":2,"text":"summary"}
{"t":"msg","msg":{"Role":"assistant","ToolCalls":[{"ID":"c1","Name":"x"}]}}
{"t":"msg","msg":{"Role":"tool","ToolCallID":"c1","Content":"r"}}
{"t":"fork","n":1}
{"t":"reset"}`)
	f.Fuzz(func(t *testing.T, s string) {
		p := fuzzProject(t)
		sess, err := p.Create()
		if err != nil {
			t.Fatal(err)
		}
		id := sess.ID
		_ = sess.Close("")
		var log bytes.Buffer
		for _, l := range bytes.Split([]byte(s), []byte("\n")) {
			if json.Valid(l) {
				log.WriteString(line(string(l)))
			}
		}
		fh, err := os.OpenFile(filepath.Join(p.sessionDir(id), "events.log"), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.Write(log.Bytes())
		fh.Close()
		s2, st, err := p.Open(id)
		if err != nil {
			return
		}
		defer s2.Close("")
		calls := map[string]bool{}
		answered := map[string]bool{}
		for i, m := range st.History {
			for _, c := range m.ToolCalls {
				calls[c.ID] = calls[c.ID] || m.Role == "assistant" // only assistant calls are sent as calls
			}
			if m.Role == "tool" {
				if !calls[m.ToolCallID] { // false or missing: no assistant made this call
					t.Fatalf("history[%d]: a tool result for %q, which no earlier call made", i, m.ToolCallID)
				}
				answered[m.ToolCallID] = true
			}
		}
		for id, asst := range calls {
			if asst && !answered[id] {
				t.Fatalf("tool call %q has no result after repair", id)
			}
		}
		for _, tn := range st.Turns {
			if tn.Hist < 0 || tn.Hist > len(st.History) {
				t.Fatalf("turn index %d outside the history (%d)", tn.Hist, len(st.History))
			}
		}
	})
}

func line(body string) string {
	return fmt.Sprintf("%08x %s\n", crc32.ChecksumIEEE([]byte(body)), body)
}

func fuzzProject(t *testing.T) *Project {
	p, err := OpenProject(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
