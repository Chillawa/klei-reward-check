package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed ui/panel.html
var panelHTML string

//go:embed ui/klei-points.png
var pointsIcon []byte

func showPanel(root string) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return err
	}
	prefix := "/" + hex.EncodeToString(key) + "/"
	host := listener.Addr().String()
	origin := "http://" + host
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var last atomic.Int64
	var actions atomic.Int32
	lang := displayLanguage(preferredLanguages())
	var opened atomic.Bool
	last.Store(time.Now().Unix())
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host || !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		last.Store(time.Now().Unix())
		switch {
		case r.Method == "GET" && r.URL.Path == prefix:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, panelPage(root, lang))
		case r.Method == "GET" && r.URL.Path == prefix+"klei-points.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pointsIcon)
		case r.Method == "GET" && r.URL.Path == prefix+"status":
			read := currentStatus
			if opened.CompareAndSwap(false, true) {
				read = openStatus
			}
			s, e := read(root)
			if e != nil {
				http.Error(w, "无法读取助手状态。", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s)
		case r.Method == "POST" && r.URL.Path == prefix+"appearance" && r.Header.Get("Origin") == origin && r.Header.Get("Content-Type") == "application/json":
			var in struct{ Theme string }
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&in) != nil || (in.Theme != "light" && in.Theme != "dark" && in.Theme != "system") {
				http.Error(w, "Invalid appearance", http.StatusBadRequest)
				return
			}
			if err := saveAppearance(root, in.Theme); err != nil {
				http.Error(w, "Could not save appearance", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && r.URL.Path == prefix+"action" && r.Header.Get("Origin") == origin && r.Header.Get("Content-Type") == "application/json":
			var in struct {
				Action string
				Gifts  bool
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&in) != nil {
				http.Error(w, "参数无效。", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if !actions.CompareAndSwap(0, 1) {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "另一次操作正在进行。"})
				return
			}
			defer func() { last.Store(time.Now().Unix()); actions.Store(0) }()
			message := ""
			if e := updateSettings(root, in.Action, in.Gifts); e != nil && !errors.Is(e, errLoginCancelled) {
				message = e.Error()
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
		default:
			http.NotFound(w, r)
		}
	})
	srv := http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		clock := time.NewTicker(30 * time.Second)
		defer clock.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-clock.C:
				if actions.Load() == 0 && time.Now().Unix()-last.Load() > 120 {
					_ = srv.Close()
					return
				}
			}
		}
	}()
	url := origin + prefix
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = quietCommand(exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url))
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if e := cmd.Start(); e != nil {
		return fmt.Errorf("无法打开默认浏览器：%w", e)
	}
	go cmd.Wait()
	err = srv.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
