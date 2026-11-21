package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const browserAccountURL = "https://accounts.klei.com/account/info"
const browserLinkURL = "https://accounts.klei.com/link/"

const visibleBrowserAccount = `(() => {
	if (location.origin !== 'https://accounts.klei.com' || !/^\/account\/info\/?$/.test(location.pathname)) return false;
	const id = /\bKU_[A-Za-z0-9_-]{6,32}\b/;
	function visible(e) {
		if (!e || !e.getClientRects().length || getComputedStyle(e).visibility !== 'visible') return false;
		for (let n=e; n; n=n.parentElement) if (Number(getComputedStyle(n).opacity) === 0) return false;
		return true;
	}
	const body=document.body;
	if (!visible(body)) return false;
	if (id.test(body.innerText)) {
		const walker=document.createTreeWalker(body, NodeFilter.SHOW_TEXT);
		while (walker.nextNode()) if (id.test(walker.currentNode.data) && visible(walker.currentNode.parentElement)) return true;
	}
	return [...body.querySelectorAll('input[readonly]')].some(e => visible(e) && id.test(e.value));
})()`

func loginBrowserExecutable() string {
	if runtime.GOOS == "windows" {
		for _, folder := range []string{"PROGRAMFILES(X86)", "PROGRAMFILES", "LOCALAPPDATA"} {
			if base := os.Getenv(folder); base != "" {
				for _, path := range []string{`Microsoft\Edge\Application\msedge.exe`, `Google\Chrome\Application\chrome.exe`, `Chromium\Application\chrome.exe`} {
					if found, err := exec.LookPath(filepath.Join(base, path)); err == nil {
						return found
					}
				}
			}
		}
	}
	for _, name := range []string{"msedge", "microsoft-edge", "microsoft-edge-stable", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if found, err := exec.LookPath(name); err == nil {
			return found
		}
	}
	return ""
}

type browserDebugEndpoint struct {
	host string
	ws   string
}

func readBrowserEndpoint(dir string) (browserDebugEndpoint, error) {
	data, err := os.ReadFile(filepath.Join(dir, "DevToolsActivePort"))
	if err != nil {
		return browserDebugEndpoint{}, err
	}
	lines := strings.Fields(string(data))
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "/devtools/browser/") || strings.ContainsAny(lines[1], "?#") {
		return browserDebugEndpoint{}, errors.New("浏览器未提供登录窗口。")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1 || port > 65535 {
		return browserDebugEndpoint{}, errors.New("浏览器登录窗口端口无效。")
	}
	host := "127.0.0.1:" + strconv.Itoa(port)
	return browserDebugEndpoint{host, "ws://" + host + lines[1]}, nil
}

type loginBrowserPage struct {
	Type                 string
	URL                  string
	WebSocketDebuggerURL string
}

func (ep browserDebugEndpoint) pages(client *http.Client) ([]loginBrowserPage, error) {
	resp, err := client.Get("http://" + ep.host + "/json/list")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("登录窗口状态 %d", resp.StatusCode)
	}
	var pages []loginBrowserPage
	err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&pages)
	return pages, err
}

func (ep browserDebugEndpoint) pagePath(page loginBrowserPage) string {
	u, err := url.Parse(page.URL)
	if err != nil || page.Type != "page" || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "accounts.klei.com") ||
		(u.Port() != "" && u.Port() != "443") || u.User != nil {
		return ""
	}
	socket, err := url.Parse(page.WebSocketDebuggerURL)
	if err != nil || socket.Scheme != "ws" || socket.Host != ep.host || socket.User != nil || !strings.HasPrefix(socket.Path, "/devtools/page/") {
		return ""
	}
	return u.EscapedPath()
}

type loginBrowserReply struct {
	ID     int
	Result json.RawMessage
	Error  *struct{ Code int }
}

type loginBrowserConnection struct {
	*websocket.Conn
	id int
}

func connectLoginBrowser(address string) (*loginBrowserConnection, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, resp, err := dialer.Dial(address, nil)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, err
	}
	conn.SetReadLimit(2 << 20)
	return &loginBrowserConnection{Conn: conn}, nil
}

func (c *loginBrowserConnection) command(method string, args any, result any) error {
	c.id++
	deadline := time.Now().Add(3 * time.Second)
	c.SetWriteDeadline(deadline)
	c.SetReadDeadline(deadline)
	if err := c.WriteJSON(map[string]any{"id": c.id, "method": method, "params": args}); err != nil {
		return err
	}
	for {
		var message loginBrowserReply
		if err := c.ReadJSON(&message); err != nil {
			return err
		}
		if message.ID != c.id {
			continue
		}
		if message.Error != nil {
			return fmt.Errorf("浏览器命令失败 (%d)", message.Error.Code)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(message.Result, result)
	}
}

type loginBrowserCookie struct {
	Domain       string          `json:"domain"`
	Path         string          `json:"path"`
	Name         string          `json:"name"`
	Value        string          `json:"value"`
	Expires      float64         `json:"expires"`
	HTTPOnly     bool            `json:"httpOnly"`
	Secure       bool            `json:"secure"`
	PartitionKey json.RawMessage `json:"partitionKey,omitempty"`
}

func (c *loginBrowserConnection) accountVisible() bool {
	var eval struct{ Result struct{ Value bool } }
	err := c.command("Runtime.evaluate", map[string]any{"expression": visibleBrowserAccount, "returnByValue": true}, &eval)
	return err == nil && eval.Result.Value
}

func (ep browserDebugEndpoint) accountSession(page loginBrowserPage) []byte {
	path := ep.pagePath(page)
	switch path {
	case "/account", "/account/", "/account/info", "/account/info/":
	default:
		return nil
	}
	c, err := connectLoginBrowser(page.WebSocketDebuggerURL)
	if err != nil {
		return nil
	}
	defer c.Close()
	if path == "/account" || path == "/account/" {
		c.command("Runtime.evaluate", map[string]any{"expression": `if(location.origin === 'https://accounts.klei.com' && /^\/account\/?$/.test(location.pathname)) location.replace('/account/info')`}, nil)
		return nil
	}
	if !c.accountVisible() {
		return nil
	}
	var payload struct {
		Cookies []loginBrowserCookie `json:"cookies"`
	}
	if c.command("Network.getCookies", map[string]any{"urls": []string{browserAccountURL, browserLinkURL}}, &payload) != nil {
		return nil
	}
	records := payload.Cookies[:0]
	for _, cookie := range payload.Cookies {
		switch strings.ToLower(cookie.Domain) {
		case "accounts.klei.com", ".accounts.klei.com", ".klei.com":
		default:
			continue
		}
		prefix := strings.TrimSuffix(cookie.Path, "/") + "/"
		if (cookie.Path != "/account/info" && cookie.Path != "/link/" && !strings.HasPrefix("/account/info", prefix) && !strings.HasPrefix("/link/", prefix)) ||
			!strings.HasPrefix(cookie.Path, "/") || cookie.Name == "" || strings.ContainsAny(cookie.Path+cookie.Name+cookie.Value, "\x00\r\n\t") ||
			(len(cookie.PartitionKey) != 0 && string(cookie.PartitionKey) != "null") {
			continue
		}
		cookie.PartitionKey = nil
		records = append(records, cookie)
	}
	if len(records) == 0 || !c.accountVisible() {
		return nil
	}
	payload.Cookies = records
	data, _ := json.Marshal(payload)
	return data
}

func closeLoginBrowser(cmd *exec.Cmd, done <-chan struct{}, ep browserDebugEndpoint) {
	select {
	case <-done:
		return
	default:
	}
	if ep.ws != "" {
		if c, err := connectLoginBrowser(ep.ws); err == nil {
			c.command("Browser.close", map[string]any{}, nil)
			c.Close()
		}
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		<-done
	}
}

func browserLogin() (session []byte, loginErr error) {
	browser := loginBrowserExecutable()
	if browser == "" {
		return nil, errors.New("请安装 Edge、Chrome 或 Chromium 后重新登录。")
	}
	dir, err := os.MkdirTemp("", "klei-login-")
	if err != nil {
		return nil, errors.New("无法创建独立的登录窗口。")
	}
	defer func() {
		// Chromium may hold the profile briefly after the final browser window closes on Windows.
		for i := 0; i < 12; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		loginErr = fmt.Errorf("无法移除临时登录目录 %s。", dir)
	}()
	cmd := exec.Command(browser, "--user-data-dir="+dir, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
		"--disable-sync", "--disable-extensions", "--disable-background-mode", "--no-first-run", "--no-default-browser-check", "--new-window", browserAccountURL)
	if err := cmd.Start(); err != nil {
		return nil, errors.New("无法打开浏览器登录窗口。")
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	var ep browserDebugEndpoint
	defer func() { closeLoginBrowser(cmd, done, ep) }()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	start := time.Now()
	seenPage := false
	var lastPage time.Time
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil, errLoginCancelled
		case <-deadline.Done():
			return nil, errors.New("等待登录超时。")
		case <-ticker.C:
			if ep.ws == "" {
				ep, err = readBrowserEndpoint(dir)
				if err != nil {
					if time.Since(start) > 25*time.Second {
						return nil, errors.New("无法连接独立的登录窗口。")
					}
					continue
				}
			}
			pages, err := ep.pages(client)
			if err != nil {
				continue
			}
			for _, page := range pages {
				if page.Type == "page" {
					seenPage = true
					lastPage = time.Now()
				}
				if data := ep.accountSession(page); data != nil {
					return data, nil
				}
			}
			if seenPage && time.Since(lastPage) > 3*time.Second {
				return nil, errLoginCancelled
			}
		}
	}
}
