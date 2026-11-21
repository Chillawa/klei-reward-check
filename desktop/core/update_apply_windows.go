package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func swapUpdatePayload(staged, target string) error { return os.Rename(staged, target) }

func updateFileInUse(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_USER_MAPPED_FILE)
}
