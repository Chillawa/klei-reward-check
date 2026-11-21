package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Standalone binaries can live in arbitrary user directories. Only recognize
// the project notice that package-desktop.sh ships next to the executable.
func packagedUpdateNotices(dir string) (string, error) {
	folder := filepath.Join(dir, "licenses")
	info, err := os.Lstat(folder)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || err == nil && !info.IsDir() {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	path := filepath.Join(folder, "Licenses.txt")
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || err == nil && !info.Mode().IsRegular() {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrPermission) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	head, err := io.ReadAll(io.LimitReader(file, 128<<10))
	if err != nil {
		return "", err
	}
	head = bytes.ReplaceAll(head, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(head, []byte("Klei Points Auto-Claim\n")) || !bytes.Contains(head, []byte("\nArtwork\n")) || !bytes.Contains(head, []byte("\nGo\n")) {
		return "", nil
	}
	return path, nil
}

func (p pendingDesktopUpdate) stageNotices(payload string) error {
	from, err := packagedUpdateNotices(filepath.Dir(payload))
	if err != nil || from == "" {
		return err
	}
	installed, err := packagedUpdateNotices(filepath.Dir(p.Target))
	if err != nil || installed == "" {
		return err
	}
	dir := filepath.Join(p.Stage, "licenses")
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	return copyUpdateFile(from, filepath.Join(dir, "Licenses.txt"), 0644)
}

func (p pendingDesktopUpdate) replaceNotices() error {
	from, err := packagedUpdateNotices(p.Stage)
	if err != nil || from == "" {
		return err
	}
	dest, err := packagedUpdateNotices(filepath.Dir(p.Target))
	if err != nil || dest == "" {
		return err
	}
	info, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0222 == 0 {
		return nil
	}
	if err = os.Chmod(from, info.Mode().Perm()); err != nil {
		return err
	}
	if err = os.Rename(from, dest); err != nil {
		return err
	}
	return syncRewardDirectory(filepath.Dir(dest))
}
