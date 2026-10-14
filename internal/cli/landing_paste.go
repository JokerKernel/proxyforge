package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const landingPasteSessionPrefix = "session-"

type landingPasteWorkspace struct {
	dir  string
	lock *os.File
}

func (w *landingPasteWorkspace) path() string {
	return filepath.Join(w.dir, "landing.json")
}

func (w *landingPasteWorkspace) close() error {
	guard, err := lockLandingPasteRoot(filepath.Dir(w.dir), false)
	if err != nil {
		return errors.Join(err, w.release())
	}
	if guard == nil {
		return w.release()
	}
	defer guard.Close()
	return w.closeLocked()
}

func (w *landingPasteWorkspace) release() error {
	if w.lock == nil {
		return nil
	}
	err := w.lock.Close()
	w.lock = nil
	return err
}

func (w *landingPasteWorkspace) closeLocked() error {
	removeErr := os.RemoveAll(w.dir)
	closeErr := w.release()
	var rootErr error
	if removeErr == nil {
		rootErr = removeEmptyLandingPasteRoot(filepath.Dir(w.dir))
	}
	if err := errors.Join(removeErr, closeErr, rootErr); err != nil {
		return fmt.Errorf("清理临时粘贴目录 %s: %w", w.dir, err)
	}
	return nil
}

func newLandingPasteWorkspace(reportFailure func(string, error)) (_ *landingPasteWorkspace, err error) {
	root := filepath.Join(os.TempDir(), fmt.Sprintf("proxyforge-landing-paste-%d", os.Geteuid()))
	guard, err := lockLandingPasteRoot(root, true)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	defer func() {
		if err != nil {
			err = errors.Join(err, removeEmptyLandingPasteRoot(root))
		}
	}()
	cleanupLandingPasteSessions(root, reportFailure)
	dir, err := os.MkdirTemp(root, landingPasteSessionPrefix)
	if err != nil {
		return nil, err
	}
	w := &landingPasteWorkspace{dir: dir}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.closeLocked())
		}
	}()
	w.lock, err = os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(w.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(w.path(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	return w, nil
}

// Serialize creation, stale cleanup and removal of an empty outer directory.
// A waiter may have opened a directory that another session has since removed;
// retry against the current path instead of using that unlinked directory lock.
func lockLandingPasteRoot(root string, create bool) (*os.File, error) {
	for {
		if create {
			if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
				return nil, fmt.Errorf("创建临时粘贴目录: %w", err)
			}
		}
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			if create {
				continue
			}
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !privatePasteDirectory(info) {
			return nil, fmt.Errorf("临时粘贴目录不是当前用户的 0700 私有目录: %s", root)
		}
		guard, err := os.Open(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX); err != nil {
			_ = guard.Close()
			return nil, fmt.Errorf("锁定临时粘贴目录: %w", err)
		}
		locked, err := guard.Stat()
		if err != nil {
			_ = guard.Close()
			return nil, err
		}
		current, err := os.Lstat(root)
		if os.IsNotExist(err) || (err == nil && !os.SameFile(locked, current)) {
			_ = guard.Close()
			continue
		}
		if err != nil {
			_ = guard.Close()
			return nil, err
		}
		if !privatePasteDirectory(current) {
			_ = guard.Close()
			return nil, fmt.Errorf("临时粘贴目录不是当前用户的 0700 私有目录: %s", root)
		}
		return guard, nil
	}
}

func removeEmptyLandingPasteRoot(root string) error {
	err := os.Remove(root)
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}

func privatePasteDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && info.Mode().Perm() == 0700 && stat.Uid == uint32(os.Geteuid())
}

// Called while holding the root directory lock. Active sessions retain an
// exclusive lease, including in the editor through an inherited descriptor.
func cleanupLandingPasteSessions(root string, reportFailure func(string, error)) {
	report := func(path string, err error) {
		if err != nil && reportFailure != nil {
			reportFailure(path, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		report(root, err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), landingPasteSessionPrefix) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			report(path, err)
			continue
		}
		if !privatePasteDirectory(info) {
			report(path, fmt.Errorf("不是当前用户的私有目录，已跳过"))
			continue
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if os.IsNotExist(err) {
			// Another session may have just finished its normal cleanup.
			continue
		}
		if err != nil {
			report(path, err)
			continue
		}
		lease := os.NewFile(uintptr(fd), path)
		info, err = lease.Stat()
		if err != nil || !privatePasteDirectory(info) {
			_ = lease.Close()
			if err == nil {
				err = fmt.Errorf("不是当前用户的私有目录，已跳过")
			}
			report(path, err)
			continue
		}
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			_ = lease.Close()
			continue
		}
		if err != nil {
			_ = lease.Close()
			report(path, err)
			continue
		}
		removeErr := os.RemoveAll(path)
		report(path, errors.Join(removeErr, lease.Close()))
	}
}
