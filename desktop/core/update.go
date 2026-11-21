package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var latestReleaseURL = "https://api.github.com/repos/Chillawa/klei-reward-check/releases/latest"

type updateCheck struct {
	Target  string    `json:"target"`
	Checked time.Time `json:"checked"`
	Error   string    `json:"error,omitempty"`
}

// Scheduled jobs run from a private copy. Only a foreground launch identifies
// the application the user actually opens.
func rememberUpdateTarget(root string) error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	core, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	target := core
	if runtime.GOOS == "darwin" {
		target = filepath.Dir(filepath.Dir(filepath.Dir(target)))
		if !strings.HasSuffix(target, ".app") || updateCorePath(target) != core {
			return errors.New("请从解压后的应用程序打开。")
		}
	}
	path := stateFile(root, "update-location.json")
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	var previous string
	if err := readState(path, &previous); err == nil && previous == target {
		return nil
	}
	return writeState(path, target)
}

func startPanelUpdate(root string) {
	if releaseVersion == "dev" || rememberUpdateTarget(root) != nil {
		return
	}
	core, err := os.Executable()
	if err == nil {
		check := quietCommand(exec.Command(core, "--check-updates"))
		if check.Start() == nil {
			_ = check.Process.Release()
		}
	}
}

func checkReleaseUpdate(root string, force bool) error {
	if releaseVersion == "dev" {
		return nil
	}
	dir := filepath.Dir(stateFile(root, "update-location.json"))
	err := locked(dir, "update-check.lock", func() error {
		var target string
		if err := readState(stateFile(root, "update-location.json"), &target); err != nil || target == "" {
			return err
		}
		if pending, err := resumePendingUpdate(root, target); err != nil || pending {
			return err
		}
		var last updateCheck
		record := stateFile(root, "update-check.json")
		if err := readState(record, &last); err != nil {
			return err
		}
		interval := 55 * time.Minute
		if last.Error != "" {
			interval = 5 * time.Minute
		}
		if age := time.Since(last.Checked); !force && last.Target == target && age >= 0 && age < interval {
			return nil
		}
		checkErr := updateFromRelease(root, target)
		last = updateCheck{Target: target, Checked: time.Now()}
		if checkErr != nil {
			last.Error = checkErr.Error()
		}
		return errors.Join(checkErr, writeState(record, last))
	})
	if errors.Is(err, errOperationBusy) && !force {
		return nil
	}
	return err
}

func updateFromRelease(root, target string) error {
	current, err := installedReleaseVersion(target)
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".release-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	payload, version, err := fetchUpdate(ctx, http.DefaultClient, latestReleaseURL, current, runtime.GOOS, runtime.GOARCH, staging)
	if err != nil || payload == "" {
		return err
	}
	return applyDownloadedUpdate(root, target, payload, version)
}
