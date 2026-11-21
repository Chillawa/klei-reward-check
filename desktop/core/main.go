package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

var releaseVersion = "dev"

func dispatch(args []string) error {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(releaseVersion)
		return nil
	}
	root, err := appDataDir()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return showPanel(root)
	}
	switch args[0] {
	case "--self-check":
		fmt.Println("ok")
		return nil
	case "--probe-login", "--test-window":
		if runtime.GOOS != "darwin" {
			return errors.New("本机没有 Mac 登录窗口。")
		}
		cmd := exec.Command(loginHelper(), args[0])
		out, err := cmd.Output()
		os.Stdout.Write(out)
		return err
	case "--status", "--open-status":
		source := currentStatus
		if args[0] == "--open-status" {
			source = openStatus
		}
		s, e := source(root)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(s)
	case "--run":
		return runWorker(root)
	case "--action":
		if len(args) == 3 && (args[2] == "0" || args[2] == "1") {
			return updateSettings(root, args[1], args[2] == "1")
		}
	}
	return errors.New("启动参数无效。")
}
func main() {
	if e := dispatch(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
