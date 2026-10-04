// Package logstore is a crash-safe append-only file of records: one
// "crc32hex payload\n" line each (payload must not contain a newline, as
// JSON never does). A torn or corrupt tail is cut off on open. A background
// writer does all I/O, so Append never waits for the disk: records are
// written promptly (surviving a process crash) and fsync'd at Sync/Flush and,
// while there are unsynced writes, at least every GroupSync (bounding what a
// power loss can take). Used by sessions and memory.
package logstore

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"
	"time"
)

// Log is an open record file.
type Log struct {
	f       *os.File
	mu      sync.Mutex
	cond    *sync.Cond
	queue   [][]byte
	syncReq uint64 // Sync/Flush requests issued
	synced  uint64 // requests satisfied
	closed  bool
	dirty   bool // written since the last fsync
	fsyncs  int
	err     error
	done    chan struct{}
	sh      *shared // nil: single-process log (sessions)
}

// GroupSync is the longest a written record waits for fsync.
var GroupSync = 3 * time.Second

// Open opens path for appending and returns its valid payloads. A torn or
// corrupt tail (crash mid-write, bad sector) is cut off, and how many bytes
// were dropped is reported.
func Open(path string) (*Log, [][]byte, int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, 0, err
	}
	recs, good, err := ReadAll(f)
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
	go l.groupCommit(GroupSync) // read here: the setting applies per log, without racing later changes
	return l, recs, dropped, nil
}

// ReadAll returns the payloads up to the first bad line and the byte offset just after the last good one.
func ReadAll(r io.Reader) ([][]byte, int64, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var recs [][]byte
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

func decodeLine(line []byte) ([]byte, bool) {
	if len(line) < 10 || line[8] != ' ' {
		return nil, false
	}
	body := line[9 : len(line)-1]
	var want uint32
	if _, err := fmt.Sscanf(string(line[:8]), "%08x", &want); err != nil || crc32.ChecksumIEEE(body) != want {
		return nil, false
	}
	return append([]byte(nil), body...), true
}

func encodeLine(body []byte) []byte {
	var b bytes.Buffer
	b.Grow(len(body) + 10)
	fmt.Fprintf(&b, "%08x ", crc32.ChecksumIEEE(body))
	b.Write(body)
	b.WriteByte('\n')
	return b.Bytes()
}

// Append queues a payload; it never blocks on I/O.
func (l *Log) Append(payload []byte) {
	line := encodeLine(payload)
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
	if l.sh != nil {
		l.sh.close()
	}
	return err
}

// groupCommit requests an fsync every interval while there are unsynced writes.
func (l *Log) groupCommit(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.mu.Lock()
			if l.dirty && l.synced == l.syncReq && !l.closed {
				l.syncReq++
				l.cond.Signal()
			}
			l.mu.Unlock()
		case <-l.done:
			return
		}
	}
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
			err = l.write(buf)
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
		if len(buf) > 0 {
			l.dirty = true
		}
		if syncNow && err == nil {
			l.dirty = false
			l.fsyncs++
		}
		if syncNow || err != nil {
			l.synced = want
		}
		l.cond.Broadcast()
	}
}

// Stats reports fsyncs done and whether unsynced writes are pending (for tests and measurement).
func (l *Log) Stats() (fsyncs int, dirty bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fsyncs, l.dirty
}
