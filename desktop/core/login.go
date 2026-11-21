package main

import (
	"bytes"
	"encoding/json"
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
)

var errLoginCancelled = errors.New("登录窗口已关闭。")

func hasSession(root string) bool {
	f, err := os.Open(filepath.Join(root, "klei.cookies"))
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "#HttpOnly_"), "\r"), "\t")
		if len(fields) == 7 && fields[0] == "accounts.klei.com" && fields[2] == "/" && fields[5] == "session" && fields[6] != "" {
			exp, err := strconv.ParseInt(fields[4], 10, 64)
			if err == nil && exp > now {
				return true
			}
		}
	}
	return false
}
func loginHelper() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "klei-login")
}
func saveLogin(root string, data []byte) error {
	var result struct {
		Cookies []struct {
			Domain, Path, Name, Value string
			Expires                   float64
			HTTPOnly                  bool `json:"httpOnly"`
		}
	}
	if len(data) > 1<<20 || json.Unmarshal(data, &result) != nil {
		return errors.New("无法读取本次 Klei 登录。")
	}
	var jar bytes.Buffer
	jar.WriteString("# Netscape HTTP Cookie File\n")
	valid := false
	for _, c := range result.Cookies {
		d := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if d != "klei.com" && d != "accounts.klei.com" || !strings.HasPrefix(c.Path, "/") || c.Name == "" || strings.ContainsAny(c.Path+c.Name+c.Value, "\t\r\n\x00") {
			continue
		}
		exp := int64(c.Expires)
		if exp < 0 {
			exp = 0
		}
		if exp != 0 && exp <= time.Now().Unix() {
			continue
		}
		if c.Name == "session" && c.Path == "/" && c.Value != "" && exp > 0 {
			valid = true
		}
		if c.HTTPOnly {
			jar.WriteString("#HttpOnly_")
		}
		fmt.Fprintf(&jar, "accounts.klei.com\tFALSE\t%s\tTRUE\t%d\t%s\t%s\n", c.Path, exp, c.Name, c.Value)
	}
	if !valid {
		return errors.New("没有收到可保存的 Klei 登录，请重试。")
	}
	return replaceFile(filepath.Join(root, "klei.cookies"), jar.Bytes())
}
func captureLogin(root string) error {
	var raw []byte
	var err error
	if runtime.GOOS == "darwin" {
		raw, err = exec.Command(loginHelper()).Output()
		var status *exec.ExitError
		if errors.As(err, &status) && status.ExitCode() == 2 {
			return errLoginCancelled
		}
	} else {
		raw, err = browserLogin()
	}
	if err != nil {
		return err
	}
	return saveLogin(root, raw)
}
