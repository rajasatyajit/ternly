package logstore

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// shared is the state of a log that several processes append to (memory: one
// project store used by every ternly session in that workspace).
//
//   - Each batch is appended (O_APPEND) under an exclusive flock on the file, so
//     batches never interleave. A fragment left by a writer that crashed
//     mid-write is terminated with a newline before the next batch, and
//     readers skip lines that fail their CRC, so one torn write never hides
//     later records.
//   - Every process holds a shared flock on path+".lock" while the log is open.
//     Only a process that finds itself alone may compact (rewrite) the file.
//     A rewrite replaces the inode; writers and readers notice and reopen, so a
//     process that raced the rewrite loses nothing.
type shared struct {
	path  string
	lock  *os.File // presence lock (".lock")
	mu    sync.Mutex
	rf    *os.File // reader: a separate open file, so its shared flock conflicts with our writer's
	roff  int64    // Tail position in rf
	rinfo os.FileInfo
}

func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if err != syscall.EINTR {
			return err
		}
	}
}

// OpenShared opens (creating if needed) a log shared between processes and
// returns its records. compact, if set, is called with the records when no
// other process has the log open; if it returns fewer, the file is rewritten
// with them (atomically: temp file, fsync, rename).
func OpenShared(path string, compact func([][]byte) [][]byte) (*Log, [][]byte, error) {
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, err
	}
	alone := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
	if !alone {
		if err := flock(lock, syscall.LOCK_SH); err != nil {
			lock.Close()
			return nil, nil, err
		}
	}
	fail := func(err error, fs ...*os.File) (*Log, [][]byte, error) {
		for _, f := range fs {
			f.Close()
		}
		lock.Close()
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fail(err)
	}
	if err := flock(f, syscall.LOCK_EX); err != nil {
		return fail(err, f)
	}
	recs, good, err := readLenient(f)
	if err != nil {
		return fail(err, f)
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > good {
		_ = f.Truncate(good) // we hold the write lock: a partial line is a crashed writer's
	}
	if alone && compact != nil {
		if kept := compact(recs); len(kept) < len(recs) {
			if nf, size, err := rewrite(path, kept); err == nil {
				f.Close()
				f, recs, good = nf, kept, size
			}
		}
	}
	_ = flock(f, syscall.LOCK_UN)
	if alone {
		_ = flock(lock, syscall.LOCK_SH) // not atomic: a racing compactor is caught by the inode check
	}
	rf, err := os.Open(path)
	if err != nil {
		return fail(err, f)
	}
	ri, _ := rf.Stat()
	l := &Log{f: f, done: make(chan struct{}), sh: &shared{path: path, lock: lock, rf: rf, roff: good, rinfo: ri}}
	l.cond = sync.NewCond(&l.mu)
	go l.writer()
	go l.groupCommit(GroupSync)
	return l, recs, nil
}

// readLenient reads complete lines from the current offset, skipping lines
// that fail their CRC. good is the end of the last complete line.
func readLenient(r io.Reader) (recs [][]byte, good int64, err error) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if err == io.EOF {
			return recs, good, nil
		}
		if err != nil {
			return nil, 0, err
		}
		if rec, ok := decodeLine(line); ok {
			recs = append(recs, rec)
		}
		good += int64(len(line))
	}
}

// rewrite replaces path with recs and returns it opened for appending.
func rewrite(path string, recs [][]byte) (*os.File, int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".compact-*")
	if err != nil {
		return nil, 0, err
	}
	w := bufio.NewWriterSize(tmp, 1<<20)
	var size int64
	for _, r := range recs {
		n, _ := w.Write(encodeLine(r))
		size += int64(n)
	}
	if err = w.Flush(); err == nil {
		err = tmp.Sync()
	}
	tmp.Close()
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return nil, 0, err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	return f, size, err
}

// write appends one batch: under the file's exclusive lock, to the file now
// at path (reopening if a compaction replaced it).
func (l *Log) write(buf []byte) error {
	if l.sh == nil {
		_, err := l.f.Write(buf)
		return err
	}
	for range 3 {
		if err := flock(l.f, syscall.LOCK_EX); err != nil {
			return err
		}
		cur, err1 := l.f.Stat()
		now, err2 := os.Stat(l.sh.path)
		if err1 == nil && err2 == nil && !os.SameFile(cur, now) {
			_ = flock(l.f, syscall.LOCK_UN)
			nf, err := os.OpenFile(l.sh.path, os.O_RDWR|os.O_APPEND, 0o600)
			if err != nil {
				return err
			}
			l.f.Close()
			l.f = nf // only the writer goroutine (and Close, after it exits) touch l.f
			continue
		}
		out := buf
		if cur != nil && cur.Size() > 0 {
			var last [1]byte
			if _, err := l.f.ReadAt(last[:], cur.Size()-1); err == nil && last[0] != '\n' {
				out = append([]byte{'\n'}, buf...) // end a crashed writer's fragment so it can't swallow ours
			}
		}
		_, err := l.f.Write(out)
		_ = flock(l.f, syscall.LOCK_UN)
		return err
	}
	return os.ErrInvalid
}

// Tail returns records appended since Open or the previous Tail, by any
// process, including this one. After another process compacted the file it
// returns every record (reset=true); records are expected to be idempotent.
func (l *Log) Tail() (recs [][]byte, reset bool, err error) {
	s := l.sh
	if s == nil {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if now, err := os.Stat(s.path); err == nil && !os.SameFile(s.rinfo, now) {
		rf, err := os.Open(s.path)
		if err != nil {
			return nil, false, err
		}
		s.rf.Close()
		s.rf, s.roff, s.rinfo, reset = rf, 0, now, true
	}
	fi, err := s.rf.Stat()
	if err != nil || fi.Size() <= s.roff {
		return nil, reset, err
	}
	if err := flock(s.rf, syscall.LOCK_SH); err != nil {
		return nil, reset, err
	}
	defer flock(s.rf, syscall.LOCK_UN)
	recs, n, err := readLenient(io.NewSectionReader(s.rf, s.roff, 1<<62))
	if err != nil {
		return nil, reset, err
	}
	s.roff += n
	return recs, reset, nil
}

func (s *shared) close() {
	s.mu.Lock()
	s.rf.Close()
	s.mu.Unlock()
	s.lock.Close()
}
