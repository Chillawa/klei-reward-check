package main

import "golang.org/x/sys/unix"

func swapUpdatePayload(staged, target string) error {
	return unix.RenamexNp(staged, target, unix.RENAME_SWAP)
}

func updateFileInUse(err error) bool { return false }
