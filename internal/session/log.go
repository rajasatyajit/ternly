package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"

	"github.com/rajasatyajit/ternly/internal/agent"
)

// Log is a crash-safe append-only record file: one "crc32hex json\n" line per
// record. A background writer does all I/O, so Record never waits for the
// disk: each record is write(2)n promptly (surviving a process crash) and
// fsync'd at Sync/Flush (surviving power loss).
type Log struct {
	f       *os.File
	mu      sync.Mutex
	cond    *sync.Cond
	queue   [][]byte
	syncReq uint64 // Sync/Flush requests issued
	synced  uint64 // requests satisfied
	closed  bool
	err     error
	done    chan struct{}
}

// openLog opens path for appending and returns its valid records. A torn or
// corrupt tail (crash mid-write, bad sector) is cut off, and how many bytes
// were dropped is reported.
func openLog(path string) (*Log, []agent.Record, int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, 0, err
	}
	recs, good, err := readRecords(f)
	if err != nil {
		f.Close()
		return nil, nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, 0, err
	}
	dropped := fi.Size() - good
	if dropped > 0 {
		if err := f.Truncate(good); err != nil {
			f.Close()
			return nil, nil, 0, err
		}
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, 0, err
	}
	l := &Log{f: f, done: make(chan struct{})}
	l.cond = sync.NewCond(&l.mu)
	go l.writer()
	return l, recs, dropped, nil
}

// readRecords returns the records up to the first bad line and the byte offset just after the last good one.
func readRecords(r io.Reader) ([]agent.Record, int64, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var recs []agent.Record
	var off int64
	for {
		line, err := br.ReadBytes('\n')
		if err == io.EOF {
			return recs, off, nil // a partial last line (no newline) is a torn write
		}
		if err != nil {
			return nil, 0, err
		}
		rec, ok := decodeLine(line)
		if !ok {
			return recs, off, nil
		}
		recs = append(recs, rec)
		off += int64(len(line))
	}
}

func decodeLine(line []byte) (agent.Record, bool) {
	var r agent.Record
	if len(line) < 10 || line[8] != ' ' {
		return r, false
	}
	body := line[9 : len(line)-1]
	var want uint32
	if _, err := fmt.Sscanf(string(line[:8]), "%08x", &want); err != nil || crc32.ChecksumIEEE(body) != want {
		return r, false
	}
	return r, json.Unmarshal(body, &r) == nil
}

func encodeLine(r agent.Record) []byte {
	body, _ := json.Marshal(r)
	var b bytes.Buffer
	b.Grow(len(body) + 10)
	fmt.Fprintf(&b, "%08x ", crc32.ChecksumIEEE(body))
	b.Write(body)
	b.WriteByte('\n')
	return b.Bytes()
}

// Record queues r; it never blocks on I/O.
func (l *Log) Record(r agent.Record) {
	line := encodeLine(r)
	l.mu.Lock()
	if !l.closed {
		l.queue = append(l.queue, line)
		l.cond.Signal()
	}
	l.mu.Unlock()
}

// Sync asks the writer to fsync after what is queued, without waiting.
func (l *Log) Sync() {
	l.mu.Lock()
	l.syncReq++
	l.cond.Signal()
	l.mu.Unlock()
}

// Flush waits until everything queued is written and fsync'd.
func (l *Log) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return l.err
	}
	l.syncReq++
	want := l.syncReq
	l.cond.Signal()
	for l.synced < want && l.err == nil {
		l.cond.Wait()
	}
	return l.err
}

// Close flushes and closes the file.
func (l *Log) Close() error {
	err := l.Flush()
	l.mu.Lock()
	l.closed = true
	l.cond.Broadcast()
	l.mu.Unlock()
	<-l.done
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (l *Log) writer() {
	defer close(l.done)
	var buf []byte
	l.mu.Lock()
	for {
		for len(l.queue) == 0 && l.synced == l.syncReq && !l.closed {
			l.cond.Wait()
		}
		if l.closed && len(l.queue) == 0 {
			l.mu.Unlock()
			return
		}
		batch, want := l.queue, l.syncReq
		l.queue = nil
		l.mu.Unlock()

		buf = buf[:0]
		for _, b := range batch {
			buf = append(buf, b...)
		}
		var err error
		if len(buf) > 0 {
			_, err = l.f.Write(buf)
		}
		syncNow := want > 0
		l.mu.Lock()
		syncNow = syncNow && l.synced < want
		l.mu.Unlock()
		if err == nil && syncNow {
			err = l.f.Sync()
		}

		l.mu.Lock()
		if err != nil && l.err == nil {
			l.err = err
		}
		if syncNow || err != nil {
			l.synced = want
		}
		l.cond.Broadcast()
	}
}
