package lane

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// A job's log keeps the start of its output and a rolling tail, so a gate that prints without end never
// fills the disk: once the file reaches MaxBytes, everything after the first HeadBytes is cut down to its
// most recent half, behind a line saying how much was cut.
const (
	HeadBytes = 256 << 10
	MaxBytes  = 16 << 20
)

// Log is a job's capped log file. It is safe for concurrent use.
type Log struct {
	mu        sync.Mutex
	path      string
	f         *os.File
	size      int64 // bytes in the file
	head      int64 // bytes kept from the start, once something has been cut
	mark      int64 // length of the line saying what was cut, after the head; 0 until something is
	cut       int64 // bytes cut so far
	headBytes int64
	maxBytes  int64
}

// CreateLog creates (or empties) the log at path, making its directory.
func CreateLog(path string) (*Log, error) { return createLog(path, HeadBytes, MaxBytes) }

func createLog(path string, head, max int64) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, f: f, headBytes: head, maxBytes: max}, nil
}

// Path is the log's file.
func (l *Log) Path() string { return l.path }

// Write appends p, cutting the middle of the log whenever it would grow past its cap.
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	written := 0
	for len(p) > 0 {
		if l.size >= l.maxBytes {
			if err := l.trim(); err != nil {
				return written, err
			}
		}
		chunk := p
		if room := l.maxBytes - l.size; int64(len(chunk)) > room {
			chunk = chunk[:room]
		}
		n, err := l.f.Write(chunk)
		l.size += int64(n)
		written += n
		p = p[n:]
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// trimAlign is how far trim looks for the start of a line to cut at.
const trimAlign = 4 << 10

// trim rewrites the log as its head, a line saying how much is cut, and the most recent half of the rest,
// starting at a line where one starts nearby. The rewrite replaces the file whole, so a reader never sees
// it half done.
func (l *Log) trim() error {
	if l.mark == 0 {
		l.head = min(l.headBytes, l.size)
	}
	start := l.head + l.mark
	from := max(l.size-(l.maxBytes-l.head)/2, start)
	tail := make([]byte, l.size-from)
	if _, err := l.f.ReadAt(tail, from); err != nil && err != io.EOF {
		return err
	}
	if i := bytes.IndexByte(tail[:min(len(tail), trimAlign)], '\n'); i >= 0 {
		tail, from = tail[i+1:], from+int64(i+1)
	}
	head := make([]byte, l.head)
	if _, err := l.f.ReadAt(head, 0); err != nil && err != io.EOF {
		return err
	}
	l.cut += from - start
	mark := fmt.Appendf(nil, "\n[baton] … %d bytes of output cut here …\n", l.cut)

	tmp, err := os.CreateTemp(filepath.Dir(l.path), "."+filepath.Base(l.path)+".tmp-*")
	if err != nil {
		return err
	}
	for _, b := range [][]byte{head, mark, tail} {
		if _, err := tmp.Write(b); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	l.f.Close() // Windows renames nothing over an open file
	renamed := os.Rename(tmp.Name(), l.path)
	if renamed != nil {
		os.Remove(tmp.Name())
	}
	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.f = f
	if renamed != nil {
		l.cut -= from - start
		return renamed
	}
	l.mark, l.size = int64(len(mark)), l.head+int64(len(mark))+int64(len(tail))
	return nil
}

// Close closes the log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Tail returns the last lines of the log at path: at most lines of them, and at most maxBytes in all. A
// line cut by the byte limit is left out, unless it is the only one there is.
func Tail(path string, lines, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	from := max(fi.Size()-int64(maxBytes), 0)
	buf := make([]byte, fi.Size()-from)
	if _, err := f.ReadAt(buf, from); err != nil && err != io.EOF {
		return "", err
	}
	text := strings.TrimRight(string(buf), "\n")
	if from > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	all := strings.Split(text, "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n"), nil
}
