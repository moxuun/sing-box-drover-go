package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Logger struct {
	mu   sync.Mutex
	file *os.File
}

func New(path string) *Logger {
	l := &Logger{}
	if path == "" {
		return l
	}
	if dir := filepath.Dir(path); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		l.file = f
	}
	return l
}

func (l *Logger) Log(section, message string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	prefix := ""
	if section != "" {
		prefix = "[" + section + "] "
	}
	if _, err := fmt.Fprintf(l.file, "%s %s%s\n", time.Now().Format("2006-01-02 15:04:05.000"), prefix, message); err != nil {
		_ = l.file.Close()
		l.file = nil
		return
	}
	_ = l.file.Sync()
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}
