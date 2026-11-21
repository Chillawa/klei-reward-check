package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func swapUpdatePayload(staged, target string) error {
	return unix.RenamexNp(staged, target, unix.RENAME_SWAP)
}

func updateFileInUse(err error) bool { return false }

func handOffUpdate(root string, p pendingDesktopUpdate) error {
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
