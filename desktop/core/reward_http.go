package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const rewardOrigin = "https://accounts.klei.com"
const profileEndpoint = "/account/profile.json"
const pointsEndpoint = "/account/rewards/items.json"
const ledgerEndpoint = "/account/transactions/data.json"

var errLoginRequired = errors.New("请重新登录 Klei。")
var errRewardPending = errors.New("等待到账确认。")
var errAccountChanged = fmt.Errorf("请使用原来的 Klei 账户核对到账。%w", errRewardPending)
var errClaimNotSent = errors.New("无法连接 Klei。")
var errRewardRead = errors.New("暂时无法核对 Klei 账户记录。")
var errRewardState = errors.New("无法读取本机领取记录。")

type rewardPage struct {
	status         int
	mime, location string
	body           []byte
}

type rewardClient struct {
	dir     string
	request func(path string, claim bool) (rewardPage, error)
}

func openRewardClient(dir string) *rewardClient {
	direct := false
	return &rewardClient{dir: dir, request: func(path string, claim bool) (rewardPage, error) {
		page, err := curlRewardPage(dir, path, direct)
		var transfer *exec.ExitError
		gatewayFailure := page.status == 502 || page.status == 503 || page.status == 504
		if runtime.GOOS == "darwin" && !claim && !direct && (errors.As(err, &transfer) || gatewayFailure) {
			page, err = curlRewardPage(dir, path, true)
			if err == nil {
				direct = true
			}
		}
		// A single URL, no redirects or retries. DNS and connect failures occur before submission.
		if claim && errors.As(err, &transfer) {
			switch transfer.ExitCode() {
			case 5, 6, 7:
				return rewardPage{}, errClaimNotSent
			}
		}
		return page, err
	}}
}

// Only the account reader may select a network route by retrying. Reward links
// are single-use requests even if curl never receives a response.
func curlRewardPage(dir, path string, direct bool) (rewardPage, error) {
	switch path {
	case profileEndpoint, pointsEndpoint, ledgerEndpoint, "/account/rewards":
	default:
		if !strings.HasPrefix(path, "/link/") || !rewardCode.MatchString(strings.TrimPrefix(path, "/link/")) {
			return rewardPage{}, errRewardRead
		}
	}
	jar := filepath.Join(dir, "klei.cookies")
	if _, err := accountCookieDigest(jar); err != nil {
		return rewardPage{}, err
	}
	temp, err := os.CreateTemp(dir, ".klei-cookies-")
	if err != nil {
		return rewardPage{}, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Close(); err != nil {
		return rewardPage{}, err
	}
	bin := "curl"
	if runtime.GOOS == "darwin" {
		bin = "/usr/bin/curl"
	}
	if runtime.GOOS == "windows" {
		if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
			bin = filepath.Join(systemRoot, "System32", "curl.exe")
		}
	}
	timeout := 45
	if path == ledgerEndpoint {
		timeout = 120
	}
	args := []string{"-q", "--silent", "--globoff", "--proto", "=https", "--compressed", "--connect-timeout", "6", "--max-time", strconv.Itoa(timeout), "--max-filesize", "2097152", "--cookie", jar, "--cookie-jar", tempName, "--header", "Accept-Language: en-US,en;q=0.9", "--write-out", "\n%{http_code}\n%{content_type}\n%{redirect_url}"}
	if direct {
		args = append(args, "--noproxy", "accounts.klei.com", "--doh-url", "https://dns.google/dns-query")
	}
	args = append(args, rewardOrigin+path)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout+3)*time.Second)
	defer cancel()
	out, err := quietCommand(exec.CommandContext(ctx, bin, args...)).Output()
	if err != nil {
		return rewardPage{}, fmt.Errorf("无法连接 Klei。%w", err)
	}
	fields := make([]string, 3)
	for i := 2; i >= 0; i-- {
		cut := bytes.LastIndexByte(out, '\n')
		if cut < 0 {
			return rewardPage{}, errRewardRead
		}
		fields[i] = string(out[cut+1:])
		out = out[:cut]
	}
	status, err := strconv.Atoi(fields[0])
	if err != nil {
		return rewardPage{}, errRewardRead
	}
	contentType, _, _ := mime.ParseMediaType(fields[1])
	// curl reports a cookie-jar write failure as a warning. Never replace the
	// original jar with the empty placeholder created before the request.
	saved, err := os.ReadFile(tempName)
	if err != nil || !bytes.HasPrefix(saved, []byte("# Netscape HTTP Cookie File")) {
		return rewardPage{}, errors.New("无法保存 Klei 登录状态。")
	}
	if err = commitRewardFile(tempName, jar); err != nil {
		return rewardPage{}, err
	}
	return rewardPage{status: status, mime: contentType, location: fields[2], body: out}, nil
}

func commitRewardFile(tempName, path string) error {
	file, err := os.OpenFile(tempName, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = file.Sync()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tempName, path); err != nil {
		return err
	}
	return syncRewardDirectory(filepath.Dir(path))
}

func syncRewardDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func rewardFileInfo(path string, directory bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	valid := info.Mode().IsRegular()
	if directory {
		valid = info.IsDir()
	}
	if !valid || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errRewardState
	}
	return info, nil
}

func accountCookieDigest(path string) (string, error) {
	if _, err := rewardFileInfo(path, false); err != nil {
		if os.IsNotExist(err) {
			return "", errLoginRequired
		}
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	values := map[[3]string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimPrefix(strings.TrimSuffix(line, "\r"), "#HttpOnly_")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) != 7 {
			return "", errors.New("Klei 登录文件格式无效。")
		}
		domain := strings.TrimLeft(strings.ToLower(fields[0]), ".")
		if domain == "klei.com" || domain == "accounts.klei.com" {
			values[[3]string{domain, fields[2], fields[5]}] = fields[6]
		}
	}
	if len(values) == 0 {
		return "", errLoginRequired
	}
	keys := make([][3]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		for k := 0; k < 3; k++ {
			if keys[i][k] != keys[j][k] {
				return keys[i][k] < keys[j][k]
			}
		}
		return false
	})
	hash := sha256.New()
	for _, key := range keys {
		for _, field := range []string{key[0], key[1], key[2], values[key]} {
			var prefix [8]byte
			binary.BigEndian.PutUint64(prefix[:], uint64(len(field)))
			hash.Write(prefix[:])
			hash.Write([]byte(field))
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func accountRedirect(page rewardPage, from string) *url.URL {
	switch page.status {
	case 301, 302, 303, 307, 308:
	default:
		return nil
	}
	if page.location == "" {
		return nil
	}
	base, _ := url.Parse(rewardOrigin + from)
	next, err := base.Parse(page.location)
	if err != nil || next.Scheme != "https" || next.Host != "accounts.klei.com" || next.User != nil || next.Fragment != "" || next.RawPath != "" {
		return nil
	}
	return next
}

func loginResponse(page rewardPage, path string) bool {
	next := accountRedirect(page, path)
	return page.status == 401 || next != nil && next.Path == "/login"
}
