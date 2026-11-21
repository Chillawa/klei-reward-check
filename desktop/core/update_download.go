package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const maxUpdateArchive = 128 << 20

type desktopReleaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type desktopRelease struct {
	Tag        string                `json:"tag_name"`
	Draft      bool                  `json:"draft"`
	Prerelease bool                  `json:"prerelease"`
	Assets     []desktopReleaseAsset `json:"assets"`
}

func desktopVersionParts(version string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) == 2 || len(parts) == 3 {
		for n, part := range parts {
			value, err := strconv.ParseUint(part, 10, 64)
			if err != nil {
				return result, fmt.Errorf("无效的软件版本 %q", version)
			}
			result[n] = value
		}
		return result, nil
	}
	return result, fmt.Errorf("无效的软件版本 %q", version)
}

func compareReleaseVersions(a, b string) (int, error) {
	left, err := desktopVersionParts(a)
	if err != nil {
		return 0, err
	}
	right, err := desktopVersionParts(b)
	if err != nil {
		return 0, err
	}
	return slices.Compare(left[:], right[:]), nil
}

func updateArchiveName(goos, goarch string) (string, error) {
	system := map[string]string{"darwin": "macOS", "windows": "Windows", "linux": "Linux"}[goos]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	if system == "" || arch == "" {
		return "", fmt.Errorf("没有适用于 %s/%s 的更新包", goos, goarch)
	}
	return "Klei-Points-Auto-Claim-" + system + "-" + arch + ".zip", nil
}

var githubLatestPath = regexp.MustCompile(`^/repos/([A-Za-z0-9_-]+)/([A-Za-z0-9_.-]+)/releases/latest$`)

type desktopReleaseSource struct {
	latest     *url.URL
	repository string
	local      bool
}

func releaseSource(raw string) (desktopReleaseSource, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return desktopReleaseSource{}, errors.New("软件更新地址无效")
	}
	host := u.Hostname()
	if (host == "localhost" || net.ParseIP(host).IsLoopback()) && (u.Scheme == "http" || u.Scheme == "https") {
		return desktopReleaseSource{latest: u, local: true}, nil
	}
	m := githubLatestPath.FindStringSubmatch(u.EscapedPath())
	if u.Scheme != "https" || u.Host != "api.github.com" || m == nil || m[2] == "." || m[2] == ".." || u.RawQuery != "" {
		return desktopReleaseSource{}, errors.New("软件更新地址必须来自 GitHub 仓库的正式发布接口")
	}
	return desktopReleaseSource{latest: u, repository: m[1] + "/" + m[2]}, nil
}

func (s desktopReleaseSource) sameLocalOrigin(u *url.URL) bool {
	return s.local && u.Scheme == s.latest.Scheme && u.Host == s.latest.Host && u.User == nil && u.Fragment == ""
}

func (s desktopReleaseSource) asset(release desktopRelease, name string) (desktopReleaseAsset, error) {
	var asset desktopReleaseAsset
	found := false
	for _, item := range release.Assets {
		if item.Name == name {
			if found {
				return asset, errors.New("发布的更新文件重名")
			}
			asset, found = item, true
		}
	}
	if !found {
		return asset, fmt.Errorf("发布中缺少 %s", name)
	}
	u, err := url.Parse(asset.URL)
	if err != nil || !(s.sameLocalOrigin(u) || (!s.local && asset.URL == "https://github.com/"+s.repository+"/releases/download/"+release.Tag+"/"+name)) {
		return asset, errors.New("更新文件地址与发布仓库不符")
	}
	return asset, nil
}

// Follow only GitHub's release storage redirects. A local fixture stays on its original origin.
func (s desktopReleaseSource) read(ctx context.Context, client *http.Client, address string, limit int64, w io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Klei-Points-Auto-Claim-Updater")
	isAPI := address == s.latest.String()
	if isAPI {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	httpClient := *client
	httpClient.Jar = nil
	httpClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		u := next.URL
		allowed := s.sameLocalOrigin(u)
		if !s.local && !isAPI && u.Scheme == "https" && u.User == nil && u.Fragment == "" {
			switch u.Host {
			case "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
				allowed = true
			}
		}
		if !allowed || len(via) >= 6 {
			return errors.New("软件更新重定向无效")
		}
		if client.CheckRedirect != nil {
			return client.CheckRedirect(next, via)
		}
		return nil
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("软件更新请求返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return 0, errors.New("更新响应超过大小限制")
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if n > limit {
		return n, errors.New("更新响应超过大小限制")
	}
	return n, err
}

func releaseDigest(value string) ([]byte, error) {
	hash, err := hex.DecodeString(value)
	if err != nil || len(hash) != sha256.Size {
		return nil, errors.New("发布的 SHA-256 校验值无效")
	}
	return hash, nil
}

func (s desktopReleaseSource) archiveDigest(ctx context.Context, client *http.Client, release desktopRelease, asset desktopReleaseAsset) ([]byte, error) {
	if asset.Digest != "" {
		value, ok := strings.CutPrefix(asset.Digest, "sha256:")
		if !ok {
			return nil, errors.New("更新包的校验算法不受支持")
		}
		return releaseDigest(value)
	}
	manifest, err := s.asset(release, "SHA256SUMS")
	if err != nil {
		return nil, err
	}
	var data bytes.Buffer
	if _, err := s.read(ctx, client, manifest.URL, 64<<10, &data); err != nil {
		return nil, err
	}
	var digest []byte
	for _, line := range strings.Split(data.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset.Name {
			continue
		}
		if digest != nil {
			return nil, errors.New("SHA256SUMS 包含重复的校验值")
		}
		digest, err = releaseDigest(fields[0])
		if err != nil {
			return nil, err
		}
	}
	if digest == nil {
		return nil, errors.New("SHA256SUMS 缺少此平台的校验值")
	}
	return digest, nil
}

func fetchUpdate(ctx context.Context, client *http.Client, latestURL, currentVersion, goos, goarch, staging string) (payloadPath, version string, err error) {
	source, err := releaseSource(latestURL)
	if err != nil {
		return "", "", err
	}
	name, err := updateArchiveName(goos, goarch)
	if err != nil {
		return "", "", err
	}
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var data bytes.Buffer
	if _, err = source.read(ctx, client, latestURL, 2<<20, &data); err != nil {
		return "", "", err
	}
	var release desktopRelease
	if err := json.Unmarshal(data.Bytes(), &release); err != nil {
		return "", "", err
	}
	if release.Draft || release.Prerelease {
		return "", "", nil
	}
	order, err := compareReleaseVersions(release.Tag, currentVersion)
	if err != nil || order <= 0 {
		return "", "", err
	}
	asset, err := source.asset(release, name)
	if err != nil {
		return "", "", err
	}
	if asset.Size < 0 || asset.Size > maxUpdateArchive {
		return "", "", errors.New("发布的更新包大小无效")
	}
	digest, err := source.archiveDigest(ctx, client, release, asset)
	if err != nil {
		return "", "", err
	}
	if staging == "" {
		return "", "", errors.New("缺少软件更新暂存目录")
	}
	staging, err = filepath.Abs(staging)
	if err != nil {
		return "", "", err
	}
	dir, err := os.MkdirTemp(staging, "update-*")
	if err != nil {
		return "", "", err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	file, err := os.CreateTemp(dir, ".archive-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(file.Name())
	hash := sha256.New()
	size, downloadErr := source.read(ctx, client, asset.URL, maxUpdateArchive, io.MultiWriter(file, hash))
	closeErr := file.Close()
	if err = errors.Join(downloadErr, closeErr); err != nil {
		return "", "", err
	}
	if (asset.Size > 0 && size != asset.Size) || !bytes.Equal(hash.Sum(nil), digest) {
		return "", "", errors.New("下载的更新包未通过 SHA-256 或大小校验")
	}
	payloadPath, err = extractDesktopArchive(ctx, file.Name(), dir, goos)
	if err != nil {
		return "", "", err
	}
	return payloadPath, strings.TrimPrefix(release.Tag, "v"), nil
}
