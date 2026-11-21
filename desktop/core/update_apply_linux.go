package main

import "os"

func swapUpdatePayload(staged, target string) error { return os.Rename(staged, target) }

func updateFileInUse(err error) bool { return false }

func handOffUpdate(root string, p pendingDesktopUpdate) error { return errUpdateStillBusy }
