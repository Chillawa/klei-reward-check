package main

import (
	"errors"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func swapUpdatePayload(staged, target string) error { return os.Rename(staged, target) }

func updateFileInUse(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_USER_MAPPED_FILE)
}

func handOffUpdate(root string, p pendingDesktopUpdate) error {
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
	cmd.SysProcAttr.CreationFlags = windows.CREATE_BREAKAWAY_FROM_JOB
	if err = cmd.Start(); errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// Some parent jobs forbid breakaway. The journal can also resume when
		// the panel closes or the next hourly task runs.
		cmd = quietCommand(exec.Command(exe, "--finish-update"))
		err = cmd.Start()
	}
	if err != nil {
		return err
	}
	return cmd.Process.Release()
}
