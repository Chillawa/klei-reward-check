package main

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func updateCorePath(target string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(target, "Contents", "Helpers", "klei-core")
	}
	return target
}

func installedReleaseVersion(target string) (string, error) {
	exe := updateCorePath(target)
	// A remembered installation can be moved or replaced by an unrelated program.
	info, err := buildinfo.ReadFile(exe)
	if err != nil || info.Path != "klei-reward-check/desktop" {
		return "", fmt.Errorf("无法识别助手程序 %s", exe)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := quietCommand(exec.CommandContext(ctx, exe, "--version")).Output()
	if err != nil {
		return "", fmt.Errorf("无法读取助手版本：%w", err)
	}
	version := strings.TrimSpace(string(out))
	if _, err = compareReleaseVersions(version, version); err != nil {
		return "", err
	}
	return version, nil
}

// The journal points to a private staging directory on the target volume. It
// outlives the download, the running panel and interruptions between swapping
// the installation and moving the scheduler to its new versioned worker.
type pendingDesktopUpdate struct {
	Target  string
	Stage   string
	Version string
}

func (p pendingDesktopUpdate) valid() bool {
	return filepath.IsAbs(p.Target) && filepath.Clean(p.Target) == p.Target &&
		filepath.Dir(p.Stage) == filepath.Dir(p.Target) && p.Stage != p.Target &&
		strings.HasPrefix(filepath.Base(p.Stage), ".klei-update-") && filepath.Clean(p.Stage) == p.Stage
}

func (p pendingDesktopUpdate) payload() string {
	return filepath.Join(p.Stage, filepath.Base(p.Target))
}

func readPendingDesktopUpdate(root string) (p pendingDesktopUpdate, err error) {
	err = readState(filepath.Join(root, "pending-update.json"), &p)
	if err == nil && p != (pendingDesktopUpdate{}) && !p.valid() {
		err = errors.New("更新安装记录无效。")
	}
	return
}

func (p pendingDesktopUpdate) clear(root string) error {
	// Remove only our private sibling directory, never the installed path.
	if err := os.RemoveAll(p.Stage); err != nil {
		return err
	}
	return os.Remove(filepath.Join(root, "pending-update.json"))
}

func copyUpdateFile(source, dest string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	return errors.Join(err, out.Close())
}

func stageUpdatePayload(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.Mkdir(to, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("更新包包含无效文件。")
		}
		mode := fs.FileMode(0644)
		if runtime.GOOS != "darwin" || info.Mode().Perm()&0111 != 0 {
			mode = 0755
		}
		return copyUpdateFile(path, to, mode)
	})
}

func canonicalUpdateTarget(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("更新安装路径无效。")
	}
	return filepath.EvalSymlinks(path)
}

// Callers may remove the original download as soon as this returns.
func applyDownloadedUpdate(root, target, payload, version string) error {
	target, err := canonicalUpdateTarget(target)
	if err != nil {
		return err
	}
	return locked(root, "update-apply.lock", func() error {
		p, err := readPendingDesktopUpdate(root)
		if err != nil {
			return err
		}
		if p.Target != "" {
			if p.Target == target {
				return p.advance(root, false)
			}
			if _, err = os.Stat(p.Target); errors.Is(err, os.ErrNotExist) {
				if err = p.clear(root); err != nil {
					return err
				}
			} else {
				return errors.New("另一处安装的更新尚未完成。")
			}
		}
		current, err := installedReleaseVersion(target)
		if err != nil {
			return err
		}
		order, err := compareReleaseVersions(version, current)
		if err != nil || order <= 0 {
			return err
		}
		stage, err := os.MkdirTemp(filepath.Dir(target), ".klei-update-")
		if err != nil {
			return fmt.Errorf("无法写入助手所在目录：%w", err)
		}
		p = pendingDesktopUpdate{target, stage, version}
		if err = stageUpdatePayload(payload, p.payload()); err == nil {
			var actual string
			actual, err = installedReleaseVersion(p.payload())
			if err == nil && actual != version {
				err = errors.New("更新包中的程序版本不符。")
			}
		}
		if err == nil {
			err = writeState(filepath.Join(root, "pending-update.json"), p)
		}
		if err != nil {
			_ = os.RemoveAll(stage)
			return err
		}
		return p.advance(root, false)
	})
}

// Resume before network checks, even if the installed version already matches.
func resumePendingUpdate(root, target string) (bool, error) {
	found := false
	target, err := canonicalUpdateTarget(target)
	if err != nil {
		return false, err
	}
	err = locked(root, "update-apply.lock", func() error {
		p, err := readPendingDesktopUpdate(root)
		if err != nil || p.Target != target {
			return err
		}
		found = true
		return p.advance(root, false)
	})
	return found, err
}

var errUpdateStillBusy = errors.New("请关闭正在使用的助手窗口，更新将在可用时继续。")

func (p pendingDesktopUpdate) advance(root string, helper bool) error {
	current, err := installedReleaseVersion(p.Target)
	if err != nil {
		return err
	}
	order, err := compareReleaseVersions(p.Version, current)
	if err != nil {
		return err
	}
	if order > 0 {
		// Recheck the staged item when resuming an interrupted update.
		info, err := os.Lstat(p.Stage)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("更新暂存目录无效。")
		}
		version, err := installedReleaseVersion(p.payload())
		if err != nil {
			return err
		}
		if version != p.Version {
			return errors.New("更新包中的程序版本不符。")
		}
		if err = swapUpdatePayload(p.payload(), p.Target); err != nil {
			if !updateFileInUse(err) {
				return err
			}
			if helper {
				return errUpdateStillBusy
			}
			return handOffUpdate(root, p)
		}
		if err = syncRewardDirectory(filepath.Dir(p.Target)); err != nil {
			return err
		}
	}
	prefs, err := readPreferences(root)
	if err != nil {
		return err
	}
	if prefs.Enabled {
		if !helper && runtime.GOOS == "darwin" && len(os.Args) > 1 && os.Args[1] == "--run" {
			return handOffUpdate(root, p)
		}
		err = ensureCurrentWorker(root, func(root string, enabled bool) error {
			return configureWorker(root, updateCorePath(p.Target), enabled, registerWorker)
		})
		if err != nil {
			if !helper && runtime.GOOS == "darwin" && errors.Is(err, errOperationBusy) {
				return handOffUpdate(root, p)
			}
			return err
		}
	}
	return p.clear(root)
}

func finishDownloadedUpdate(root string) error {
	err := locked(root, "update-finish.lock", func() error {
		deadline := time.Now().Add(10 * time.Minute)
		for {
			err := locked(root, "update-apply.lock", func() error {
				p, err := readPendingDesktopUpdate(root)
				if err != nil || p.Target == "" {
					return err
				}
				return p.advance(root, true)
			})
			if !errors.Is(err, errUpdateStillBusy) && !errors.Is(err, errOperationBusy) {
				return err
			}
			if time.Now().After(deadline) {
				return err
			}
			time.Sleep(2 * time.Second)
		}
	})
	if errors.Is(err, errOperationBusy) {
		return nil
	}
	return err
}

func handOffUpdate(root string, p pendingDesktopUpdate) error {
	if runtime.GOOS == "darwin" {
		// An independent LaunchAgent can safely boot out the old hourly job.
		// Keep its definition inside the profile, so it is never auto-loaded at login.
		id := fmt.Sprintf("com.kleirewardhelper.update.%x", sha256.Sum256([]byte(root)))
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		job := domain + "/" + id
		file := filepath.Join(root, "update-helper.plist")
		exe := updateCorePath(p.Target)
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>` + id +
			`</string><key>ProgramArguments</key><array><string>` + xmlText(exe) + `</string><string>--finish-update</string></array>` +
			`<key>EnvironmentVariables</key><dict><key>HOME</key><string>` + xmlText(home) +
			`</string></dict><key>RunAtLoad</key><true/></dict></plist>`
		// Reuse the same job and executable path on successive updates. If the
		// user moved the app, replace the idle job's definition under our lock.
		body, _ := os.ReadFile(file)
		if _, err = commandResult("/bin/launchctl", "print", job); err == nil {
			if string(body) == plist {
				_, err = commandResult("/bin/launchctl", "kickstart", job)
				return err
			}
			if _, err = commandResult("/bin/launchctl", "bootout", job); err != nil {
				return err
			}
		}
		if err = replaceFile(file, []byte(plist)); err != nil {
			return err
		}
		_, err = commandResult("/bin/launchctl", "bootstrap", domain, file)
		return err
	}
	// A Windows helper must run from a third path: whichever image is running
	// is mapped, including an executable launched directly out of staging.
	image, err := identifyWorker(updateCorePath(p.payload()))
	if err != nil {
		return err
	}
	exe, err := image.copyTo(root)
	if err != nil {
		return err
	}
	cmd := quietCommand(exec.Command(exe, "--finish-update"))
	if err = cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
