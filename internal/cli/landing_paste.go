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
	removeErr := os.RemoveAll(w.dir)
	var closeErr error
	if w.lock != nil {
		closeErr = w.lock.Close()
		w.lock = nil
	}
	if err := errors.Join(removeErr, closeErr); err != nil {
		return fmt.Errorf("清理临时粘贴目录 %s: %w", w.dir, err)
	}
	return nil
}

func newLandingPasteWorkspace(reportFailure func(string, error)) (_ *landingPasteWorkspace, err error) {
	root := filepath.Join(os.TempDir(), fmt.Sprintf("proxyforge-landing-paste-%d", os.Geteuid()))
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("创建临时粘贴目录: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !privatePasteDirectory(info) {
		return nil, fmt.Errorf("临时粘贴目录不是当前用户的 0700 私有目录: %s", root)
	}
	guard, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	// Serialize session creation and stale cleanup, including recovery from a
	// crash between creating a session directory and acquiring its lease.
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("锁定临时粘贴目录: %w", err)
	}
	cleanupLandingPasteSessions(root, reportFailure)
	dir, err := os.MkdirTemp(root, landingPasteSessionPrefix)
	if err != nil {
		return nil, err
	}
	w := &landingPasteWorkspace{dir: dir}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.close())
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
