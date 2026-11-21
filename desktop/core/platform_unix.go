//go:build !windows

package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"
)

func quietCommand(cmd *exec.Cmd) *exec.Cmd { return cmd }
func lockFile(file *os.File) error         { return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) }

func preferredLanguages() []string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/bin/defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			names := strings.FieldsFunc(string(out), func(c rune) bool {
				return c == ',' || c == '(' || c == ')' || c == '"' || unicode.IsSpace(c)
			})
			if len(names) > 0 {
				return names
			}
		}
	}
	for _, key := range []string{"LC_ALL", "LANGUAGE", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(key); value != "" {
			return strings.Split(value, ":")
		}
	}
	return nil
}
