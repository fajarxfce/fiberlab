package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"ftthlab/internal/images"
	"golang.org/x/sys/unix"
)

// The journal and privileged logs live in a root-owned directory. Only each
// router's disk/socket directory is writable by the unprivileged QEMU process.
func (e *Engine) PrepareDirectory() error {
	path, err := filepath.Abs(e.Dir)
	if err != nil {
		return err
	}
	current := string(os.PathSeparator)
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err = os.Mkdir(current, 0711); err != nil {
				return err
			}
			if err = os.Chmod(current, 0711); err != nil {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("runtime directory must have root-owned, non-writable ancestors without symlinks: %s", current)
		}
	}
	e.Dir = path
	return os.Chmod(path, 0711)
}

func (e *Engine) lockRuntime() (func(), error) {
	file, err := os.OpenFile(filepath.Join(e.Dir, "helper.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("a helper already owns this runtime; stop it before cleanup or restart")
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
}

func (e *Engine) CleanupStale(ctx context.Context) error {
	if err := e.PrepareDirectory(); err != nil {
		return err
	}
	release, err := e.lockRuntime()
	if err != nil {
		return err
	}
	defer release()
	return e.Recover(ctx)
}

func (e *Engine) cacheImage(input images.Image) (images.Image, error) {
	digest, err := hex.DecodeString(input.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return input, fmt.Errorf("invalid CHR SHA-256 digest")
	}
	dir := filepath.Join(e.Dir, "images")
	if err = os.MkdirAll(dir, 0711); err != nil {
		return input, err
	}
	if err = os.Chmod(dir, 0711); err != nil {
		return input, err
	}
	file, err := os.Open(input.Path)
	if err != nil {
		return input, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 32<<20 || info.Size() > 1<<30 {
		return input, fmt.Errorf("CHR must be a regular raw image of 32 MiB–1 GiB")
	}
	// Copy and hash the same open file, then make the cached backing image
	// immutable to the application owner before an unprivileged VM reads it.
	copy, err := os.CreateTemp(dir, ".verify-")
	if err != nil {
		return input, err
	}
	defer os.Remove(copy.Name())
	defer copy.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(copy, hash), io.LimitReader(file, 1<<30+1))
	if err != nil || n != info.Size() || hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(input.SHA256) {
		return input, fmt.Errorf("CHR image digest or size changed; import the image again")
	}
	if err = copy.Sync(); err != nil {
		return input, err
	}
	if err = copy.Chmod(0444); err != nil {
		return input, err
	}
	if err = copy.Close(); err != nil {
		return input, err
	}
	path := filepath.Join(dir, strings.ToLower(input.SHA256)+".img")
	if err = os.Rename(copy.Name(), path); err != nil {
		return input, err
	}
	input.Path, input.Size = path, n
	return input, nil
}

type boundedLog struct {
	mu   sync.Mutex
	file *os.File
	path string
	size int64
	max  int64
}

func openLog(path string) (*boundedLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &boundedLog{file: file, path: path, size: info.Size(), max: 128 << 10}, nil
}

func (l *boundedLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(data)
	if l.size+int64(n) > l.max {
		if err := l.file.Close(); err != nil {
			return 0, err
		}
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			return 0, err
		}
		var err error
		l.file, err = os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return 0, err
		}
		l.size = 0
	}
	if int64(len(data)) > l.max {
		data = data[int64(len(data))-l.max:]
	}
	written, err := l.file.Write(data)
	l.size += int64(written)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (l *boundedLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
