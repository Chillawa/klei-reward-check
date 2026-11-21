package announcements

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sourceUserAgent = "Klei-Points-Auto-Claim/1.0 (public sources)"

var errSourceBrowserChallenge = errors.New("HTTP 403，网站要求浏览器验证")

func sourceURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host != u.Hostname() || u.Host == "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(raw, "\\\r\n\t ") {
		return nil
	}
	return u
}

type sourceReader struct {
	recordRewardLink func(string)
	rewardTypes      func() (wikiRewardTable, error)
	dir              string
	client           *http.Client
	now              time.Time
	ctx              context.Context
	hints            map[string]bool
}

func (r sourceReader) recent(stamp time.Time) bool {
	today := r.now.UTC().Truncate(24 * time.Hour)
	return !stamp.IsZero() && !stamp.Before(today.AddDate(0, 0, -29)) && stamp.Before(today.AddDate(0, 0, 1))
}
func (r sourceReader) cached(raw string, allow func(*url.URL) bool, maxAge time.Duration) ([]byte, string) {
	if r.dir == "" || sourceURL(raw) == nil || !allow(sourceURL(raw)) {
		return nil, ""
	}
	path := filepath.Join(r.dir, fmt.Sprintf("%x", sha256.Sum256([]byte(raw))))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > sourceBodyLimit+1024 {
		return nil, ""
	}
	if age := r.now.Sub(info.ModTime()); age < 0 || age >= maxAge {
		return nil, ""
	}
	data, err := os.ReadFile(path)
	line, body, ok := bytes.Cut(data, []byte("\n"))
	if err != nil || !ok || sourceURL(string(line)) == nil || !allow(sourceURL(string(line))) {
		return nil, ""
	}
	return body, string(line)
}
func (r sourceReader) read(raw string, allow func(*url.URL) bool) ([]byte, string, error) {
	u := sourceURL(raw)
	if u == nil || !allow(u) {
		return nil, "", errors.New("公开来源地址不匹配")
	}
	if body, final := r.cached(raw, allow, 30*time.Minute); body != nil {
		return body, final, nil
	}
	client := *r.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || sourceURL(req.URL.String()) == nil || !allow(req.URL) {
			return errors.New("公开来源跳转到未认可页面")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(r.ctx, "GET", raw, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", sourceUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden && strings.EqualFold(resp.Header.Get("Cf-Mitigated"), "challenge") {
		return nil, "", errSourceBrowserChallenge
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, sourceBodyLimit+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > sourceBodyLimit {
		return nil, "", errors.New("公开来源超过大小上限")
	}
	final := resp.Request.URL.String()
	if r.dir != "" && len(final) < 900 {
		if f, err := os.CreateTemp(r.dir, "source-"); err == nil {
			_, e := fmt.Fprintln(f, final)
			if e == nil {
				_, e = f.Write(data)
			}
			closeErr := f.Close()
			if e == nil && closeErr == nil {
				path := filepath.Join(r.dir, fmt.Sprintf("%x", sha256.Sum256([]byte(raw))))
				_ = os.Rename(f.Name(), path)
			}
			_ = os.Remove(f.Name())
		}
	}
	return data, final, nil
}
func (r sourceReader) exact(raw string) ([]byte, error) {
	data, _, err := r.read(raw, func(u *url.URL) bool { return u.String() == raw })
	return data, err
}

func sourceAbsolute(base, ref string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	v, err := u.Parse(ref)
	if err != nil {
		return ""
	}
	return v.String()
}

type sourceArticle struct {
	url, title string
	day        time.Time
}

func (r sourceReader) articles(rows []sourceArticle, limit int) []sourceArticle {
	// Cover busy boards across successive polls without downloading every thread at once.
	if len(rows) <= limit {
		return rows
	}
	pick := make([]sourceArticle, 0, limit)
	start := int(r.now.Unix()/3600) * limit % len(rows)
	for _, prioritize := range []bool{true, false} {
		for i := 0; i < len(rows); i++ {
			row := rows[(start+i)%len(rows)]
			if r.hints[row.url] == prioritize {
				pick = append(pick, row)
				if len(pick) == limit {
					return pick
				}
			}
		}
	}
	return pick
}
