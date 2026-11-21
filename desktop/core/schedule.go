package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

//go:embed windows/scheduled-task.ps1
var taskScript string

const macTask = "com.kleirewardhelper.background"
const linuxTask = "klei-reward-helper"

func commandResult(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	data, err := quietCommand(exec.CommandContext(ctx, name, args...)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", filepath.Base(name), strings.TrimSpace(string(data)), err)
	}
	return strings.TrimSpace(string(data)), nil
}

// Versioned executable paths also permit updating a running Windows task.
type workerImage struct {
	source, id, checksum string
	feed                 []byte
}

func identifyWorker(source string) (workerImage, error) {
	image := workerImage{source: source}
	f, err := os.Open(source)
	if err != nil {
		return image, err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, f); err != nil {
		return image, err
	}
	image.checksum = hex.EncodeToString(digest.Sum(nil))
	dir := filepath.Dir(source)
	feed, err := os.Open(filepath.Join(dir, "public-feed.txt"))
	if errors.Is(err, os.ErrNotExist) {
		feed, err = os.Open(filepath.Join(dir, "..", "Resources", "public-feed.txt"))
	}
	if err == nil {
		defer feed.Close()
		image.feed, err = io.ReadAll(io.LimitReader(feed, 2049))
		if len(image.feed) > 2048 {
			return image, errors.New("奖励来源配置过大。")
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return image, err
	}
	fmt.Fprintf(digest, "\x00%d\x00", len(image.feed))
	digest.Write(image.feed)
	image.id = hex.EncodeToString(digest.Sum(nil))
	return image, nil
}

func (image workerImage) path(root string) string {
	name := "klei-worker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, "bin", image.id, name)
}

func (image workerImage) copyTo(root string) (string, error) {
	dest := image.path(root)
	if info, err := os.Lstat(dest); err == nil && info.Mode().IsRegular() {
		return dest, nil
	}
	dir := filepath.Dir(dest)
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	src, err := os.Open(image.source)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(dir, ".worker-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	digest := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, digest), src)
	if err == nil && hex.EncodeToString(digest.Sum(nil)) != image.checksum {
		err = errors.New("助手已更新，请重新打开。")
	}
	if err == nil {
		err = tmp.Chmod(0700)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if len(image.feed) > 0 {
		if err = replaceFile(filepath.Join(dir, "public-feed.txt"), image.feed); err != nil {
			return "", err
		}
	}
	if err = os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	return dest, syncRewardDirectory(dir)
}

func xmlText(s string) string { var b bytes.Buffer; xml.EscapeText(&b, []byte(s)); return b.String() }
func macSchedule(dir, exe string, enable bool) error {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	target := domain + "/" + macTask
	path := filepath.Join(dir, macTask+".plist")
	_, err := commandResult("/bin/launchctl", "print", target)
	loaded := err == nil
	old, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if !enable {
		if loaded {
			if _, err = commandResult("/bin/launchctl", "bootout", target); err != nil {
				return err
			}
		}
		if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>` + macTask + `</string><key>ProgramArguments</key><array><string>` + xmlText(exe) + `</string><string>--run</string></array><key>StartInterval</key><integer>3600</integer><key>RunAtLoad</key><true/><key>ProcessType</key><string>Background</string></dict></plist>`)
	if loaded && bytes.Equal(body, old) {
		return nil
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if loaded {
		// Replacing a loaded job can stop it immediately. Both panel-driven
		// migrations and automatic updates let outstanding claims finish first.
		root, err := appDataDir()
		if err != nil {
			return err
		}
		err = locked(filepath.Dir(stateFile(root, "automatic.json")), "background.lock", func() error {
			_, e := commandResult("/bin/launchctl", "bootout", target)
			return e
		})
		if err != nil {
			return err
		}
	}
	if err = replaceFile(path, body); err == nil {
		_, err = commandResult("/bin/launchctl", "enable", target)
	}
	if err == nil {
		_, err = commandResult("/bin/launchctl", "bootstrap", domain, path)
	}
	if err != nil {
		if old == nil {
			_ = os.Remove(path)
		} else if restore := replaceFile(path, old); restore != nil {
			return errors.Join(err, restore)
		} else if loaded {
			_, restore = commandResult("/bin/launchctl", "bootstrap", domain, path)
			return errors.Join(err, restore)
		}
	}
	return err
}
func unitArg(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s) + `"`
}
func linuxSchedule(dir, exe string, enable bool) error {
	service := filepath.Join(dir, linuxTask+".service")
	timer := filepath.Join(dir, linuxTask+".timer")
	ctl := func(args ...string) error {
		_, e := commandResult("systemctl", append([]string{"--user"}, args...)...)
		return e
	}
	if !enable {
		if _, e := os.Stat(timer); errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e := ctl("disable", "--now", filepath.Base(timer)); e != nil {
			return e
		}
		for _, p := range []string{timer, service} {
			if e := os.Remove(p); e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
		}
		return ctl("daemon-reload")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	oldService, _ := os.ReadFile(service)
	oldTimer, _ := os.ReadFile(timer)
	svc := "[Unit]\nDescription=Klei Points Auto-Claim\n[Service]\nType=oneshot\nExecStart=:" + unitArg(exe) + " --run\n"
	clock := "[Unit]\nDescription=Klei Points Auto-Claim\n[Timer]\nOnStartupSec=1min\nOnCalendar=hourly\nPersistent=true\n[Install]\nWantedBy=timers.target\n"
	e := replaceFile(service, []byte(svc))
	if e == nil {
		e = replaceFile(timer, []byte(clock))
	}
	if e == nil {
		e = ctl("daemon-reload")
	}
	if e == nil {
		e = ctl("enable", "--now", filepath.Base(timer))
	}
	if e != nil {
		for p, old := range map[string][]byte{service: oldService, timer: oldTimer} {
			if old == nil {
				_ = os.Remove(p)
			} else {
				_ = replaceFile(p, old)
			}
		}
		_ = ctl("daemon-reload")
	}
	return e
}
func windowsSchedule(exe string, enable bool) error {
	action := "disable"
	if enable {
		action = "enable"
	}
	body, _ := json.Marshal(map[string]string{"action": action, "executable": exe, "arguments": "--run"})
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, utf16.Encode([]rune(taskScript)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := quietCommand(exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(buf.Bytes())))
	cmd.Stdin = bytes.NewReader(body)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("无法更改 Windows 计划任务：%w", err)
	}
	return nil
}

// The marker is written only after the OS accepts the new task. A failed
// migration can therefore be retried on the next panel opening.
func configureWorker(root, source string, enable bool, register func(string, bool) error) error {
	marker := stateFile(root, "native-worker.json")
	if !enable {
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return register("", false)
	}
	image, err := identifyWorker(source)
	if err != nil {
		return err
	}
	var active string
	if err = readState(marker, &active); err != nil {
		return err
	}
	if active == image.id {
		if info, e := os.Lstat(image.path(root)); e == nil && info.Mode().IsRegular() {
			return nil
		}
	}
	dest, err := image.copyTo(root)
	if err != nil {
		return err
	}
	if err = register(dest, true); err != nil {
		return err
	}
	return writeState(marker, image.id)
}

func applySchedule(root string, enable bool) error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	return configureWorker(root, source, enable, registerWorker)
}
func registerWorker(exe string, enable bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return macSchedule(filepath.Join(home, "Library", "LaunchAgents"), exe, enable)
	case "windows":
		return windowsSchedule(exe, enable)
	default:
		conf, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		return linuxSchedule(filepath.Join(conf, "systemd", "user"), exe, enable)
	}
}
func notifyUser(message string) {
	lang := displayLanguage(preferredLanguages())
	title := notificationText(lang, "Klei 点数自动领")
	message = notificationText(lang, message)
	switch runtime.GOOS {
	case "darwin":
		_, _ = commandResult("/usr/bin/osascript", "-e", "on run argv", "-e", `display notification (item 1 of argv) with title (item 2 of argv)`, "-e", "end run", "--", message, title)
	case "linux":
		_, _ = commandResult("notify-send", title, message)
	case "windows":
		cmd := quietCommand(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; $n=New-Object Windows.Forms.NotifyIcon; $n.Icon=[Drawing.SystemIcons]::Information; $n.Visible=$true; $n.ShowBalloonTip(4000,$env:KLEI_TITLE,$env:KLEI_NOTICE,[Windows.Forms.ToolTipIcon]::Info); Start-Sleep 5; $n.Dispose()`))
		cmd.Env = append(os.Environ(), "KLEI_NOTICE="+message, "KLEI_TITLE="+title)
		_ = cmd.Run()
	}
}
