package system

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type privateJSONFile struct {
	path string
	data []byte
}

type privateFileSnapshot struct {
	file   privateJSONFile
	mode   os.FileMode
	exists bool
}

func readPrivateFile(path string) (privateFileSnapshot, error) {
	snapshot := privateFileSnapshot{file: privateJSONFile{path: path}}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return snapshot, fmt.Errorf("配置文件权限不安全 %s（当前 %o，要求 0600）", path, info.Mode().Perm())
	}
	snapshot.mode, snapshot.exists = info.Mode().Perm(), true
	snapshot.file.data, err = os.ReadFile(path)
	return snapshot, err
}

func loadPrivateJSON(path string, value any) (bool, error) {
	snapshot, err := readPrivateFile(path)
	if err != nil || !snapshot.exists {
		return false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(snapshot.file.data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return false, fmt.Errorf("配置文件损坏 %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("配置文件包含多余或无效内容: %s", path)
	}
	return true, nil
}

func preparePrivateJSON(path string, value any) (privateJSONFile, error) {
	b, err := json.MarshalIndent(value, "", "  ")
	return privateJSONFile{path: path, data: append(b, '\n')}, err
}

// writePrivateFiles restores completed writes if any later write fails.
func writePrivateFiles(files []privateJSONFile) error {
	snapshots := make([]privateFileSnapshot, len(files))
	for i, file := range files {
		var err error
		snapshots[i], err = readPrivateFile(file.path)
		if err != nil {
			return err
		}
	}
	for i, file := range files {
		if err := writePrivateFile(file); err != nil {
			failures := []error{fmt.Errorf("保存 %s: %w", file.path, err)}
			for j := i - 1; j >= 0; j-- {
				snapshot := snapshots[j]
				var restoreErr error
				if snapshot.exists {
					restoreErr = AtomicWrite(snapshot.file.path, snapshot.file.data, snapshot.mode)
				} else {
					restoreErr = removeOptionalFile(snapshot.file.path)
				}
				if restoreErr != nil {
					failures = append(failures, fmt.Errorf("恢复 %s: %w", snapshot.file.path, restoreErr))
				}
			}
			return errors.Join(failures...)
		}
	}
	return nil
}

func writePrivateFile(file privateJSONFile) error {
	if err := os.MkdirAll(filepath.Dir(file.path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(file.path), 0700); err != nil {
		return err
	}
	return AtomicWrite(file.path, file.data, 0600)
}

func removeOptionalFile(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
