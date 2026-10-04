package graph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// watcher turns inotify events into dirty files. Events are queued by the
// kernel before the changing syscall returns, so draining the queue at the
// start of a refresh sees every change made before it — no sleeps, no races.
type watcher struct {
	mu   sync.Mutex // serialises draining between the background loop and refresh
	fd   int
	dirs map[int]string
	buf  []byte
}

const watchMask = unix.IN_CLOSE_WRITE | unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_DELETE_SELF

// watchInit registers inotify watches on the workspace (before the first
// build, so no edit is missed); false means scans are needed instead.
func (s *Service) watchInit() bool {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return false
	}
	w := &watcher{fd: fd, dirs: map[int]string{}, buf: make([]byte, 64<<10)}
	if !w.add(s.Root, s.Root) {
		unix.Close(fd)
		return false
	}
	s.dmu.Lock()
	s.watching, s.w = true, w
	s.dmu.Unlock()
	return true
}

// watchLoop drains events as they arrive until ctx ends.
func (s *Service) watchLoop(ctx context.Context) {
	s.dmu.Lock()
	w := s.w
	s.dmu.Unlock()
	if w == nil {
		return
	}
	defer func() {
		s.dmu.Lock()
		s.watching, s.w = false, nil
		s.dmu.Unlock()
		w.mu.Lock()
		unix.Close(w.fd)
		w.mu.Unlock()
	}()
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}
		if n, err := unix.Poll(fds, 500); err != nil && err != unix.EINTR {
			return
		} else if n > 0 {
			s.drain()
		}
	}
}

// drain reads every queued event and marks the files it names dirty.
func (s *Service) drain() {
	s.dmu.Lock()
	w := s.w
	s.dmu.Unlock()
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		n, err := unix.Read(w.fd, w.buf)
		if err != nil || n <= 0 { // EAGAIN: drained
			return
		}
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&w.buf[off]))
			name := strings.TrimRight(string(w.buf[off+unix.SizeofInotifyEvent:off+unix.SizeofInotifyEvent+int(ev.Len)]), "\x00")
			off += unix.SizeofInotifyEvent + int(ev.Len)
			dir := w.dirs[int(ev.Wd)]
			path := filepath.Join(dir, name)
			mark := ""
			switch {
			case ev.Mask&unix.IN_Q_OVERFLOW != 0:
				mark = "*"
			case ev.Mask&unix.IN_ISDIR != 0 && ev.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0:
				if !skipDir(name) {
					w.add(path, s.Root)
				}
				mark = "*" // files may have landed before the watch was added: rescan
			case ev.Mask&unix.IN_ISDIR != 0:
				mark = "*" // a directory went away or moved
			case strings.HasSuffix(name, ".go") && dir != "":
				if rel, err := filepath.Rel(s.Root, path); err == nil {
					mark = filepath.ToSlash(rel)
				}
			}
			if mark != "" {
				s.dmu.Lock()
				s.dirty[mark] = true
				s.dmu.Unlock()
			}
		}
	}
}

// add watches dir and its subdirectories; false when out of watches.
func (w *watcher) add(dir, root string) bool {
	ok := true
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		wd, err := unix.InotifyAddWatch(w.fd, p, watchMask)
		if err != nil { // ENOSPC: out of watches
			ok = false
			return filepath.SkipAll
		}
		w.dirs[wd] = p
		return nil
	})
	return ok
}
