package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked indicates that another process holds the workspace lock.
var ErrLocked = errors.New("workspace is locked")

// Lock is an advisory, cross-process workspace lock. The lock file is deliberately
// retained on release: unlinking it would allow two processes to lock different
// inodes at the same path. Callers must use the same stable path for a workspace,
// preferably outside its directory so cleanup can remove that directory.
type Lock struct{ file *os.File }

// AcquireLock obtains an exclusive, nonblocking lock at path. Lock files are
// private regular files; symlinks are refused. The caller must call Unlock.
func AcquireLock(path string) (*Lock, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("lock path must be absolute: %q", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("open workspace lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("stat workspace lock: %w", err)
		}
		return nil, fmt.Errorf("workspace lock is not a regular file: %s", path)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock workspace: %w", err)
	}
	return &Lock{file: file}, nil
}

// Unlock releases a lock. It is safe to call more than once.
func (l *Lock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("unlock workspace: %w", err)
	}
	return closeErr
}
