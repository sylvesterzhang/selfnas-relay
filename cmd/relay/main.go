package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed web/*
var webFiles embed.FS

type server struct {
	db       *sql.DB
	secret   []byte
	sessions map[string]time.Time
	data     *relayDataPlane
	mu       sync.Mutex
}

type apiResult struct {
	Code int         `json:"code"`
	Data interface{} `json:"data,omitempty"`
	Msg  string      `json:"msg,omitempty"`
}

func main() {
	addr := env("RELAY_LISTEN_ADDR", "0.0.0.0")
	port := env("RELAY_LISTEN_PORT", "8080")
	dsn := env("RELAY_SQLITE_PATH", "data/relay.db")
	if err := os.MkdirAll(filepath.Dir(dsn), 0750); err != nil {
		log.Fatal(err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		log.Fatal(err)
	}

	s := &server{db: db, sessions: make(map[string]time.Time)}
	if err := s.initDB(context.Background()); err != nil {
		log.Fatal(err)
	}
	s.data = newRelayDataPlane(s)
	webFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(webFS)))
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/setup/status", s.setupStatus)
	mux.HandleFunc("/api/setup/password", s.setupPassword)
	mux.HandleFunc("/api/login", s.login)
	mux.HandleFunc("/api/logout", s.logout)
	mux.HandleFunc("/api/me", s.requireAdmin(s.me))
	mux.HandleFunc("/api/password/change", s.requireAdmin(s.changePassword))
	mux.HandleFunc("/api/enrollment/requests", s.enrollmentCreate)
	mux.HandleFunc("/api/enrollment/requests/", s.enrollmentStatus)
	mux.HandleFunc("/api/applications", s.requireAdmin(s.applications))
	mux.HandleFunc("/api/applications/", s.requireAdmin(s.applicationAction))
	mux.HandleFunc("/api/nodes", s.requireAdmin(s.nodes))
	mux.HandleFunc("/api/nodes/", s.requireAdmin(s.nodeAction))
	mux.HandleFunc("/api/settings/limits", s.requireAdmin(s.limits))
	mux.HandleFunc("/v1/node/connect", s.data.connect)
	mux.HandleFunc("/n/", s.data.forward)
	// SelfNAS 前端历史上会从脚本里直接请求根路径的缩略图/封面/图标。
	// 仅兜底代理这些只读资源路径；管理 API（如 /api/login、/api/applications）不接管。
	for _, prefix := range []string{"/api/fs/", "/api/music/", "/assets/"} {
		mux.HandleFunc(prefix, s.data.forwardRoot)
	}

	listen := addr + ":" + port
	log.Printf("selfnas-relay listening on %s", listen)
	log.Fatal(http.ListenAndServe(listen, logging(mux)))
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (s *server) initDB(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS relay_admins (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS relay_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS relay_enrollment_requests (id TEXT PRIMARY KEY, request_token_hash TEXT NOT NULL, device_name TEXT NOT NULL, hostname TEXT, platform TEXT, version TEXT, public_key TEXT, status TEXT NOT NULL, node_id TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, expires_at TEXT NOT NULL, decided_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS relay_nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT NOT NULL UNIQUE, token_hash TEXT NOT NULL, device_name TEXT NOT NULL, hostname TEXT, platform TEXT, version TEXT, status TEXT NOT NULL, bandwidth_bytes INTEGER NOT NULL DEFAULT 10485760, max_connections INTEGER NOT NULL DEFAULT 8, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, last_seen_at TEXT, revoked_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS relay_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM relay_meta WHERE key='token_secret'`).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		encoded = base64.RawURLEncoding.EncodeToString(secret)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO relay_meta(key,value) VALUES('token_secret',?)`, encoded); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	s.secret, err = base64.RawURLEncoding.DecodeString(encoded)
	return err
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, apiResult{Data: map[string]string{"status": "ok"}})
}

func (s *server) setupStatus(w http.ResponseWriter, r *http.Request) {
	var count int
	if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(1) FROM relay_admins`).Scan(&count); err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "读取初始化状态失败"})
		return
	}
	jsonOut(w, 200, apiResult{Data: map[string]bool{"setup_required": count == 0}})
}

func localOnly(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *server) setupPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !localOnly(r) {
		jsonOut(w, 403, apiResult{Code: 1, Msg: "首次初始化只能在服务器本机完成"})
		return
	}
	var input struct {
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "请求格式错误"})
		return
	}
	if len([]rune(input.NewPassword)) < 8 {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "新密码至少 8 位"})
		return
	}
	if input.NewPassword != input.ConfirmPassword {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "两次输入的新密码不一致"})
		return
	}
	var count int
	if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(1) FROM relay_admins`).Scan(&count); err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "读取初始化状态失败"})
		return
	}
	if count != 0 {
		jsonOut(w, 409, apiResult{Code: 1, Msg: "管理员已经初始化"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "密码保存失败"})
		return
	}
	if _, err = s.db.ExecContext(r.Context(), `INSERT INTO relay_admins(username,password_hash,must_change_password) VALUES(?,?,0)`, env("RELAY_ADMIN_USER", "admin"), string(hash)); err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "初始化管理员失败"})
		return
	}
	jsonOut(w, 200, apiResult{Data: map[string]bool{"ok": true}})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "请求格式错误"})
		return
	}
	var hash string
	var mustChange bool
	err := s.db.QueryRowContext(r.Context(), `SELECT password_hash,must_change_password FROM relay_admins WHERE username=?`, input.Username).Scan(&hash, &mustChange)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
		jsonOut(w, 401, apiResult{Code: 1, Msg: "账号或密码错误"})
		return
	}
	token := randomToken(32)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	jsonOut(w, 200, apiResult{Data: map[string]interface{}{"ok": true, "must_change_password": mustChange}})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("relay_session"); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: "", Path: "/", MaxAge: -1})
	jsonOut(w, 200, apiResult{Data: map[string]bool{"ok": true}})
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	var mustChange bool
	_ = s.db.QueryRowContext(r.Context(), `SELECT must_change_password FROM relay_admins ORDER BY id LIMIT 1`).Scan(&mustChange)
	jsonOut(w, 200, apiResult{Data: map[string]interface{}{"role": "admin", "must_change_password": mustChange}})
}

func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "请求格式错误"})
		return
	}
	if len([]rune(input.NewPassword)) < 8 {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "新密码至少 8 位"})
		return
	}
	if input.NewPassword != input.ConfirmPassword {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "两次输入的新密码不一致"})
		return
	}
	var id int
	var hash string
	var mustChange bool
	if err := s.db.QueryRowContext(r.Context(), `SELECT id,password_hash,must_change_password FROM relay_admins ORDER BY id LIMIT 1`).Scan(&id, &hash, &mustChange); err != nil {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "当前密码错误"})
		return
	}
	// 首次登录已通过登录密码认证，默认密码不再重复填写；正常改密仍必须校验当前密码。
	if !mustChange && bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.CurrentPassword)) != nil {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "当前密码错误"})
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "密码保存失败"})
		return
	}
	if _, err = s.db.ExecContext(r.Context(), `UPDATE relay_admins SET password_hash=?,must_change_password=0 WHERE id=?`, string(newHash), id); err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "密码保存失败"})
		return
	}
	jsonOut(w, 200, apiResult{Data: map[string]bool{"ok": true}})
}

func (s *server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("relay_session")
		if err != nil {
			jsonOut(w, 401, apiResult{Code: 1, Msg: "未登录"})
			return
		}
		s.mu.Lock()
		expires, ok := s.sessions[cookie.Value]
		if ok && time.Now().After(expires) {
			delete(s.sessions, cookie.Value)
			ok = false
		}
		s.mu.Unlock()
		if !ok {
			jsonOut(w, 401, apiResult{Code: 1, Msg: "未登录"})
			return
		}
		var mustChange bool
		_ = s.db.QueryRowContext(r.Context(), `SELECT must_change_password FROM relay_admins ORDER BY id LIMIT 1`).Scan(&mustChange)
		if mustChange && r.URL.Path != "/api/me" && r.URL.Path != "/api/password/change" {
			jsonOut(w, 428, apiResult{Code: 2, Msg: "首次登录必须修改控制台密码"})
			return
		}
		next(w, r)
	}
}

func (s *server) enrollmentCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input struct {
		DeviceName string `json:"device_name"`
		Hostname   string `json:"hostname"`
		Platform   string `json:"platform"`
		Version    string `json:"version"`
		PublicKey  string `json:"public_key"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || strings.TrimSpace(input.DeviceName) == "" {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "缺少设备名称"})
		return
	}
	requestID, requestToken := randomToken(18), randomToken(32)
	expires := time.Now().UTC().Add(30 * time.Minute)
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO relay_enrollment_requests(id,request_token_hash,device_name,hostname,platform,version,public_key,status,expires_at) VALUES(?,?,?,?,?,?,?,'pending',?)`, requestID, sha(requestToken), input.DeviceName, input.Hostname, input.Platform, input.Version, input.PublicKey, expires)
	if err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "创建申请失败"})
		return
	}
	jsonOut(w, 200, apiResult{Data: map[string]interface{}{"request_id": requestID, "request_token": requestToken, "status": "pending", "expires_at": expires}})
}

func (s *server) enrollmentStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/enrollment/requests/")
	provided := r.Header.Get("X-Request-Token")
	if id == "" || provided == "" {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "申请凭证不完整"})
		return
	}
	var tokenHash, status, nodeID, expiresRaw string
	err := s.db.QueryRowContext(r.Context(), `SELECT request_token_hash,status,COALESCE(node_id,''),expires_at FROM relay_enrollment_requests WHERE id=?`, id).Scan(&tokenHash, &status, &nodeID, &expiresRaw)
	if err != nil || !hmac.Equal([]byte(tokenHash), []byte(sha(provided))) {
		jsonOut(w, 404, apiResult{Code: 1, Msg: "申请不存在"})
		return
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, expiresRaw)
	if parseErr != nil {
		expires, parseErr = time.Parse("2006-01-02 15:04:05", expiresRaw)
	}
	if status == "pending" && parseErr == nil && time.Now().UTC().After(expires) {
		status = "expired"
		_, _ = s.db.ExecContext(r.Context(), `UPDATE relay_enrollment_requests SET status='expired' WHERE id=?`, id)
	}
	data := map[string]interface{}{"status": status}
	if status == "approved" && nodeID != "" {
		data["node_id"] = nodeID
		data["node_token"] = derive(s.secret, nodeID)
		data["route_path"] = "/n/" + nodeID + "/"
	}
	jsonOut(w, 200, apiResult{Data: data})
}

func (s *server) applications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,device_name,hostname,platform,version,status,created_at,expires_at FROM relay_enrollment_requests ORDER BY created_at DESC`)
	if err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: err.Error()})
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var id, device, host, platform, version, status, created, expires string
		if rows.Scan(&id, &device, &host, &platform, &version, &status, &created, &expires) == nil {
			out = append(out, map[string]interface{}{"id": id, "device_name": device, "hostname": host, "platform": platform, "version": version, "status": status, "created_at": created, "expires_at": expires})
		}
	}
	jsonOut(w, 200, apiResult{Data: out})
}

func (s *server) applicationAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	id, action := parts[2], parts[3]
	var device, host, platform, version, status, nodeID string
	if err := s.db.QueryRowContext(r.Context(), `SELECT device_name,hostname,platform,version,status,COALESCE(node_id,'') FROM relay_enrollment_requests WHERE id=?`, id).Scan(&device, &host, &platform, &version, &status, &nodeID); err != nil {
		jsonOut(w, 404, apiResult{Code: 1, Msg: "申请不存在"})
		return
	}
	if action == "delete" {
		if nodeID != "" {
			if s.data != nil {
				s.data.revoke(nodeID)
			}
			_, _ = s.db.ExecContext(r.Context(), `UPDATE relay_nodes SET status='revoked',revoked_at=CURRENT_TIMESTAMP WHERE node_id=?`, nodeID)
		}
		if _, err := s.db.ExecContext(r.Context(), `DELETE FROM relay_enrollment_requests WHERE id=?`, id); err != nil {
			jsonOut(w, 500, apiResult{Code: 1, Msg: "删除申请失败"})
			return
		}
		jsonOut(w, 200, apiResult{Data: map[string]string{"status": "deleted"}})
		return
	}
	if status != "pending" {
		jsonOut(w, 404, apiResult{Code: 1, Msg: "申请不存在或已处理"})
		return
	}
	if action == "reject" {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE relay_enrollment_requests SET status='rejected',decided_at=CURRENT_TIMESTAMP WHERE id=?`, id)
		jsonOut(w, 200, apiResult{Data: map[string]string{"status": "rejected"}})
		return
	}
	if action != "approve" {
		http.NotFound(w, r)
		return
	}
	nodeID = randomToken(18)
	bandwidth, maxConnections := s.currentLimits(r.Context())
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO relay_nodes(node_id,token_hash,device_name,hostname,platform,version,status,bandwidth_bytes,max_connections) VALUES(?,?,?,?,?,?,'approved',?,?)`, nodeID, sha(derive(s.secret, nodeID)), device, host, platform, version, bandwidth, maxConnections)
	if err == nil {
		_, err = s.db.ExecContext(r.Context(), `UPDATE relay_enrollment_requests SET status='approved',node_id=?,decided_at=CURRENT_TIMESTAMP WHERE id=?`, nodeID, id)
	}
	if err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "批准申请失败"})
		return
	}
	jsonOut(w, 200, apiResult{Data: map[string]string{"status": "approved", "node_id": nodeID}})
}

func (s *server) nodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT node_id,device_name,hostname,platform,version,status,bandwidth_bytes,max_connections,created_at,last_seen_at FROM relay_nodes ORDER BY created_at DESC`)
	if err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: err.Error()})
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var id, device, host, platform, version, status, created, last string
		var bandwidth int64
		var maxConnections int
		if rows.Scan(&id, &device, &host, &platform, &version, &status, &bandwidth, &maxConnections, &created, &last) == nil {
			out = append(out, map[string]interface{}{"node_id": id, "device_name": device, "hostname": host, "platform": platform, "version": version, "status": status, "bandwidth_bytes": bandwidth, "max_connections": maxConnections, "created_at": created, "last_seen_at": last})
		}
	}
	jsonOut(w, 200, apiResult{Data: out})
}

func (s *server) nodeAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || r.Method != http.MethodPost || parts[3] != "revoke" {
		http.NotFound(w, r)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE relay_nodes SET status='revoked',revoked_at=CURRENT_TIMESTAMP WHERE node_id=?`, parts[2]); err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "撤销失败"})
		return
	}
	s.data.revoke(parts[2])
	jsonOut(w, 200, apiResult{Data: map[string]string{"status": "revoked"}})
}

func (s *server) currentLimits(ctx context.Context) (int64, int) {
	bandwidth, maxConnections := int64(10485760), 8
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM relay_settings`)
	if err != nil {
		return bandwidth, maxConnections
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if rows.Scan(&key, &value) != nil {
			continue
		}
		switch key {
		case "default_bandwidth_bytes":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil && v >= 0 {
				bandwidth = v
			}
		case "max_connections":
			if v, err := strconv.Atoi(value); err == nil && v > 0 {
				maxConnections = v
			}
		}
	}
	return bandwidth, maxConnections
}

func (s *server) limits(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rows, _ := s.db.QueryContext(r.Context(), `SELECT key,value FROM relay_settings`)
		defer rows.Close()
		settings := map[string]string{"default_bandwidth_bytes": "10485760", "max_connections": "8"}
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				settings[k] = v
			}
		}
		jsonOut(w, 200, apiResult{Data: settings})
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input map[string]string
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "请求格式错误"})
		return
	}
	for _, key := range []string{"default_bandwidth_bytes", "max_connections"} {
		value, ok := input[key]
		if !ok {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || value == "" || (key == "default_bandwidth_bytes" && parsed < 0) || (key == "max_connections" && parsed < 1) {
			jsonOut(w, 400, apiResult{Code: 1, Msg: "限速参数无效"})
			return
		}
		if _, err := s.db.ExecContext(r.Context(), `INSERT INTO relay_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			jsonOut(w, 500, apiResult{Code: 1, Msg: "保存失败"})
			return
		}
	}
	bandwidth, maxConnections := s.currentLimits(r.Context())
	// 管理台中的限速是服务级默认值，保存时同步作用到已有节点；否则旧节点仍沿用 relay_nodes 的旧值。
	if _, err := s.db.ExecContext(r.Context(), `UPDATE relay_nodes SET bandwidth_bytes=?,max_connections=? WHERE status!='revoked'`, bandwidth, maxConnections); err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "应用节点限速失败"})
		return
	}
	s.data.mu.RLock()
	active := make([]*relayNode, 0, len(s.data.nodes))
	for _, node := range s.data.nodes {
		active = append(active, node)
	}
	s.data.mu.RUnlock()
	for _, node := range active {
		node.throttleMu.Lock()
		node.bandwidth = bandwidth
		node.throttleMu.Unlock()
		node.mu.Lock()
		node.maxConnections = maxConnections
		node.mu.Unlock()
	}
	jsonOut(w, 200, apiResult{Data: map[string]interface{}{"default_bandwidth_bytes": bandwidth, "max_connections": maxConnections}})
}

func derive(secret []byte, value string) string {
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func sha(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
func randomToken(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func jsonOut(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started))
	})
}
