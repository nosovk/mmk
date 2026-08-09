package lockfile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Lock struct {
	file *os.File
}

// Acquire obtains an exclusive advisory lock, polling so context cancellation
// can abort a wait without leaving an open descriptor.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		locked, err := tryLock(file)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if locked {
			return &Lock{file: file}, nil
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlock(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
