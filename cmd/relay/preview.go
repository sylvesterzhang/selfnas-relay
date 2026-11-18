package main

import (
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type previewState struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	apps     []map[string]interface{}
	nodes    []map[string]interface{}
	limits   map[string]string
}

// runPreview 只用于本机看管理台，不连接数据库、不保存数据，也不替代 SQL Server 正式模式。
func runPreview(addr, port string) {
	state := &previewState{
		sessions: map[string]time.Time{},
		apps:     []map[string]interface{}{{"id": "preview-request-1", "device_name": "张欢的 Windows 电脑", "hostname": "DESKTOP-PREVIEW", "platform": "windows-amd64", "version": "0.21.4", "status": "pending", "created_at": time.Now().Add(-8 * time.Minute).Format(time.RFC3339), "expires_at": time.Now().Add(22 * time.Minute).Format(time.RFC3339)}},
		nodes:    []map[string]interface{}{{"node_id": "node_preview_01", "device_name": "演示节点", "hostname": "RELAY-PREVIEW", "platform": "linux-amd64", "version": "0.21.4", "status": "online", "bandwidth_bytes": int64(10485760), "max_connections": 8, "created_at": time.Now().Add(-24 * time.Hour).Format(time.RFC3339), "last_seen_at": time.Now().Format(time.RFC3339)}},
		limits:   map[string]string{"default_bandwidth_bytes": "10485760", "max_connections": "8"},
	}
	staticFS, _ := fs.Sub(webFiles, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, apiResult{Data: map[string]string{"status": "preview"}})
	})
	mux.HandleFunc("/api/login", state.previewLogin)
	mux.HandleFunc("/api/logout", state.previewLogout)
	mux.HandleFunc("/api/me", state.previewAuth(state.previewMe))
	mux.HandleFunc("/api/applications", state.previewAuth(state.previewApplications))
	mux.HandleFunc("/api/applications/", state.previewAuth(state.previewApplicationAction))
	mux.HandleFunc("/api/nodes", state.previewAuth(state.previewNodes))
	mux.HandleFunc("/api/nodes/", state.previewAuth(state.previewNodeAction))
	mux.HandleFunc("/api/settings/limits", state.previewAuth(state.previewLimits))
	listen := addr + ":" + port
	log.Printf("selfnas-relay PREVIEW listening on %s", listen)
	log.Fatal(http.ListenAndServe(listen, logging(mux)))
}

func (p *previewState) previewLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input struct{ Username, Password string }
	if json.NewDecoder(r.Body).Decode(&input) != nil || subtle.ConstantTimeCompare([]byte(input.Username), []byte("admin")) != 1 || subtle.ConstantTimeCompare([]byte(input.Password), []byte("preview")) != 1 {
		jsonOut(w, 401, apiResult{Code: 1, Msg: "预览账号：admin / preview"})
		return
	}
	token := randomToken(24)
	p.mu.Lock()
	p.sessions[token] = time.Now().Add(8 * time.Hour)
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 28800})
	jsonOut(w, 200, apiResult{Data: map[string]bool{"ok": true}})
}
func (p *previewState) previewLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("relay_session"); err == nil {
		p.mu.Lock()
		delete(p.sessions, c.Value)
		p.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: "", Path: "/", MaxAge: -1})
	jsonOut(w, 200, apiResult{Data: map[string]bool{"ok": true}})
}
func (p *previewState) previewAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("relay_session")
		if err != nil {
			jsonOut(w, 401, apiResult{Code: 1, Msg: "未登录"})
			return
		}
		p.mu.Lock()
		expires, ok := p.sessions[c.Value]
		p.mu.Unlock()
		if !ok || time.Now().After(expires) {
			jsonOut(w, 401, apiResult{Code: 1, Msg: "未登录"})
			return
		}
		next(w, r)
	}
}
func (p *previewState) previewMe(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, apiResult{Data: map[string]string{"role": "admin", "mode": "preview"}})
}
func (p *previewState) previewApplications(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	jsonOut(w, 200, apiResult{Data: p.apps})
}
func (p *previewState) previewNodes(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	jsonOut(w, 200, apiResult{Data: p.nodes})
}
func (p *previewState) previewApplicationAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, app := range p.apps {
		if app["id"] == parts[2] {
			app["status"] = map[string]string{"approve": "approved", "reject": "rejected"}[parts[3]]
			break
		}
	}
	jsonOut(w, 200, apiResult{Data: map[string]string{"status": parts[3]}})
}
func (p *previewState) previewNodeAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, node := range p.nodes {
		if node["node_id"] == parts[2] {
			node["status"] = "revoked"
		}
	}
	jsonOut(w, 200, apiResult{Data: map[string]string{"status": "revoked"}})
}
func (p *previewState) previewLimits(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Method == http.MethodPut {
		var input map[string]string
		if json.NewDecoder(r.Body).Decode(&input) == nil {
			for k, v := range input {
				p.limits[k] = v
			}
		}
	}
	jsonOut(w, 200, apiResult{Data: p.limits})
}
