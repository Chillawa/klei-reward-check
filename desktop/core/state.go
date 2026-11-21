package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

var errOperationBusy = errors.New("另一次操作正在进行，请稍后再试。")

func appDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		if root := os.Getenv("LOCALAPPDATA"); root != "" {
			return filepath.Join(root, "KleiRewardCheck"), nil
		}
		return filepath.Join(home, "AppData", "Local", "KleiRewardCheck"), nil
	}
	return filepath.Join(home, ".klei-reward-check"), nil
}
func stateFile(root, name string) string { return filepath.Join(root, "monitor-klei", name) }
func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("助手数据目录权限异常。")
	}
	return nil
}
func readState(path string, value any) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return errors.New("助手设置文件无效。")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
func replaceFile(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".saving-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return err
	}
	return syncRewardDirectory(filepath.Dir(path))
}
func writeState(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return replaceFile(path, raw)
}
func locked(root, name string, action func() error) error {
	if err := ensurePrivateDir(root); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := lockFile(file); err != nil {
		return errOperationBusy
	}
	return action()
}
