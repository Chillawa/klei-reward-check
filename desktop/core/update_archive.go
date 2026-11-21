package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func updatePackageLayout(goos string) (root string, programs []string, required []string) {
	if goos == "darwin" {
		root = "Klei 点数自动领.app"
		programs = []string{root + "/Contents/MacOS/klei-rewards", root + "/Contents/Helpers/klei-core", root + "/Contents/Helpers/klei-login"}
		return root, programs, append([]string{root + "/Contents/Info.plist"}, programs...)
	}
	root = "klei-points-auto-claim"
	if goos == "windows" {
		root += ".exe"
	}
	return root, []string{root}, []string{root}
}

func safeUpdatePath(name string) (string, error) {
	if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:<>\"|?*") || strings.IndexFunc(name, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return "", errors.New("更新包包含无效路径")
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return "", errors.New("更新包包含无效文件名")
		}
	}
	// Localize additionally rejects platform-specific hazards, including reserved Windows device names.
	return filepath.Localize(name)
}

func extractDesktopArchive(ctx context.Context, archive, dir, goos string) (string, error) {
	zipped, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer zipped.Close()
	if len(zipped.File) > 2048 {
		return "", errors.New("更新包文件数超过限制")
	}
	root, programs, required := updatePackageLayout(goos)
	seen := make(map[string]bool, len(zipped.File))
	remaining := uint64(256 << 20)
	for _, entry := range zipped.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := strings.TrimSuffix(entry.Name, "/")
		local, err := safeUpdatePath(name)
		if err != nil {
			return "", err
		}
		key := strings.ToLower(name)
		if seen[key] {
			return "", errors.New("更新包包含重复的文件路径")
		}
		seen[key] = true
		top, _, _ := strings.Cut(name, "/")
		if top != root && !(goos != "darwin" && top == "licenses") {
			return "", errors.New("更新包结构与目标平台不符")
		}
		mode := entry.Mode()
		target := filepath.Join(dir, local)
		if mode.IsDir() && entry.UncompressedSize64 == 0 {
			if err := os.MkdirAll(target, 0755); err != nil {
				return "", err
			}
			continue
		}
		if !mode.IsRegular() || entry.UncompressedSize64 > remaining || entry.UncompressedSize64 > maxUpdateArchive {
			return "", errors.New("更新包包含无效的文件类型或大小")
		}
		remaining -= entry.UncompressedSize64
		perm := os.FileMode(0644)
		if mode.Perm()&0111 != 0 || slices.Contains(programs, name) {
			perm = 0755
		}
		if err := extractUpdateFile(entry, target, perm); err != nil {
			return "", err
		}
	}
	for _, path := range required {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", fmt.Errorf("更新包缺少有效的 %s", path)
		}
	}
	return filepath.Join(dir, root), nil
}

func extractUpdateFile(entry *zip.File, target string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	src, err := entry.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	size, copyErr := io.Copy(dst, io.LimitReader(src, int64(entry.UncompressedSize64)+1))
	if err := errors.Join(copyErr, dst.Close()); err != nil {
		return err
	}
	if uint64(size) != entry.UncompressedSize64 {
		return errors.New("更新包文件的实际大小与声明不符")
	}
	return nil
}
