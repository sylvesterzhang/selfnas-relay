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
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/microsoft/go-mssqldb"
	"golang.org/x/crypto/bcrypt"
)

//go:embed web/*
var webFiles embed.FS

type server struct {
	db       *sql.DB
	secret   []byte
	sessions map[string]time.Time
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
	if os.Getenv("RELAY_PREVIEW") == "1" {
		runPreview(addr, port)
		return
	}
	dsn := os.Getenv("RELAY_SQLSERVER_URL")
	if dsn == "" {
		log.Fatal("RELAY_SQLSERVER_URL is required")
	}

	db, err := sql.Open("sqlserver", dsn)
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
	if err := s.ensureAdmin(context.Background()); err != nil {
		log.Fatal(err)
	}

	webFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(webFS)))
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/login", s.login)
	mux.HandleFunc("/api/logout", s.logout)
	mux.HandleFunc("/api/me", s.requireAdmin(s.me))
	mux.HandleFunc("/api/enrollment/requests", s.enrollmentCreate)
	mux.HandleFunc("/api/enrollment/requests/", s.enrollmentStatus)
	mux.HandleFunc("/api/applications", s.requireAdmin(s.applications))
	mux.HandleFunc("/api/applications/", s.requireAdmin(s.applicationAction))
	mux.HandleFunc("/api/nodes", s.requireAdmin(s.nodes))
	mux.HandleFunc("/api/nodes/", s.requireAdmin(s.nodeAction))
	mux.HandleFunc("/api/settings/limits", s.requireAdmin(s.limits))

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
		`IF OBJECT_ID('relay_admins','U') IS NULL CREATE TABLE relay_admins (id INT IDENTITY PRIMARY KEY, username NVARCHAR(128) NOT NULL UNIQUE, password_hash NVARCHAR(255) NOT NULL, created_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME())`,
		`IF OBJECT_ID('relay_meta','U') IS NULL CREATE TABLE relay_meta ([key] NVARCHAR(128) PRIMARY KEY, value NVARCHAR(MAX) NOT NULL)`,
		`IF OBJECT_ID('relay_enrollment_requests','U') IS NULL CREATE TABLE relay_enrollment_requests (id NVARCHAR(64) PRIMARY KEY, request_token_hash CHAR(64) NOT NULL, device_name NVARCHAR(255) NOT NULL, hostname NVARCHAR(255) NULL, platform NVARCHAR(128) NULL, version NVARCHAR(64) NULL, public_key NVARCHAR(1024) NULL, status NVARCHAR(32) NOT NULL, node_id NVARCHAR(64) NULL, created_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(), expires_at DATETIME2 NOT NULL, decided_at DATETIME2 NULL)`,
		`IF OBJECT_ID('relay_nodes','U') IS NULL CREATE TABLE relay_nodes (id INT IDENTITY PRIMARY KEY, node_id NVARCHAR(64) NOT NULL UNIQUE, token_hash CHAR(64) NOT NULL, device_name NVARCHAR(255) NOT NULL, hostname NVARCHAR(255) NULL, platform NVARCHAR(128) NULL, version NVARCHAR(64) NULL, status NVARCHAR(32) NOT NULL, bandwidth_bytes BIGINT NOT NULL DEFAULT 10485760, max_connections INT NOT NULL DEFAULT 8, created_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(), last_seen_at DATETIME2 NULL, revoked_at DATETIME2 NULL)`,
		`IF OBJECT_ID('relay_settings','U') IS NULL CREATE TABLE relay_settings ([key] NVARCHAR(128) PRIMARY KEY, value NVARCHAR(255) NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM relay_meta WHERE [key]='token_secret'`).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		encoded = base64.RawURLEncoding.EncodeToString(secret)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO relay_meta([key],value) VALUES('token_secret',@p1)`, encoded); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	s.secret, err = base64.RawURLEncoding.DecodeString(encoded)
	return err
}

func (s *server) ensureAdmin(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM relay_admins`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	user := env("RELAY_ADMIN_USER", "admin")
	password := os.Getenv("RELAY_ADMIN_PASSWORD")
	if password == "" {
		return fmt.Errorf("RELAY_ADMIN_PASSWORD is required on first startup")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO relay_admins(username,password_hash) VALUES(@p1,@p2)`, user, string(hash))
	return err
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, apiResult{Data: map[string]string{"status": "ok"}})
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
	err := s.db.QueryRowContext(r.Context(), `SELECT password_hash FROM relay_admins WHERE username=@p1`, input.Username).Scan(&hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
		jsonOut(w, 401, apiResult{Code: 1, Msg: "账号或密码错误"})
		return
	}
	token := randomToken(32)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "relay_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	jsonOut(w, 200, apiResult{Data: map[string]bool{"ok": true}})
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
	jsonOut(w, 200, apiResult{Data: map[string]string{"role": "admin"}})
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
		next(w, r)
	}
}

func (s *server) enrollmentCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input struct{ DeviceName, Hostname, Platform, Version, PublicKey string }
	if json.NewDecoder(r.Body).Decode(&input) != nil || strings.TrimSpace(input.DeviceName) == "" {
		jsonOut(w, 400, apiResult{Code: 1, Msg: "缺少设备名称"})
		return
	}
	requestID, requestToken := randomToken(18), randomToken(32)
	expires := time.Now().UTC().Add(30 * time.Minute)
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO relay_enrollment_requests(id,request_token_hash,device_name,hostname,platform,version,public_key,status,expires_at) VALUES(@p1,@p2,@p3,@p4,@p5,@p6,@p7,'pending',@p8)`, requestID, sha(requestToken), input.DeviceName, input.Hostname, input.Platform, input.Version, input.PublicKey, expires)
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
	var tokenHash, status, nodeID string
	var expires time.Time
	err := s.db.QueryRowContext(r.Context(), `SELECT request_token_hash,status,ISNULL(node_id,''),expires_at FROM relay_enrollment_requests WHERE id=@p1`, id).Scan(&tokenHash, &status, &nodeID, &expires)
	if err != nil || !hmac.Equal([]byte(tokenHash), []byte(sha(provided))) {
		jsonOut(w, 404, apiResult{Code: 1, Msg: "申请不存在"})
		return
	}
	if status == "pending" && time.Now().UTC().After(expires) {
		status = "expired"
		_, _ = s.db.ExecContext(r.Context(), `UPDATE relay_enrollment_requests SET status='expired' WHERE id=@p1`, id)
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
		var id, device, host, platform, version, status string
		var created, expires time.Time
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
	var device, host, platform, version string
	if err := s.db.QueryRowContext(r.Context(), `SELECT device_name,hostname,platform,version FROM relay_enrollment_requests WHERE id=@p1 AND status='pending'`, id).Scan(&device, &host, &platform, &version); err != nil {
		jsonOut(w, 404, apiResult{Code: 1, Msg: "申请不存在或已处理"})
		return
	}
	if action == "reject" {
		_, _ = s.db.ExecContext(r.Context(), `UPDATE relay_enrollment_requests SET status='rejected',decided_at=SYSUTCDATETIME() WHERE id=@p1`, id)
		jsonOut(w, 200, apiResult{Data: map[string]string{"status": "rejected"}})
		return
	}
	if action != "approve" {
		http.NotFound(w, r)
		return
	}
	nodeID := randomToken(18)
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO relay_nodes(node_id,token_hash,device_name,hostname,platform,version,status) VALUES(@p1,@p2,@p3,@p4,@p5,@p6,'approved')`, nodeID, sha(derive(s.secret, nodeID)), device, host, platform, version)
	if err == nil {
		_, err = s.db.ExecContext(r.Context(), `UPDATE relay_enrollment_requests SET status='approved',node_id=@p1,decided_at=SYSUTCDATETIME() WHERE id=@p2`, nodeID, id)
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
		var id, device, host, platform, version, status string
		var bandwidth int64
		var maxConnections int
		var created, last sql.NullTime
		if rows.Scan(&id, &device, &host, &platform, &version, &status, &bandwidth, &maxConnections, &created, &last) == nil {
			out = append(out, map[string]interface{}{"node_id": id, "device_name": device, "hostname": host, "platform": platform, "version": version, "status": status, "bandwidth_bytes": bandwidth, "max_connections": maxConnections, "created_at": created.Time, "last_seen_at": last.Time})
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
	if _, err := s.db.ExecContext(r.Context(), `UPDATE relay_nodes SET status='revoked',revoked_at=SYSUTCDATETIME() WHERE node_id=@p1`, parts[2]); err != nil {
		jsonOut(w, 500, apiResult{Code: 1, Msg: "撤销失败"})
		return
	}
	jsonOut(w, 200, apiResult{Data: map[string]string{"status": "revoked"}})
}

func (s *server) limits(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rows, _ := s.db.QueryContext(r.Context(), `SELECT [key],value FROM relay_settings`)
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
		if _, err := strconv.ParseInt(value, 10, 64); err != nil || value == "" {
			jsonOut(w, 400, apiResult{Code: 1, Msg: "限速参数无效"})
			return
		}
		_, err := s.db.ExecContext(r.Context(), `MERGE relay_settings AS t USING (SELECT @p1 AS [key], @p2 AS value) AS x ON t.[key]=x.[key] WHEN MATCHED THEN UPDATE SET value=x.value WHEN NOT MATCHED THEN INSERT([key],value) VALUES(x.[key],x.value);`, key, value)
		if err != nil {
			jsonOut(w, 500, apiResult{Code: 1, Msg: "保存失败"})
			return
		}
	}
	jsonOut(w, 200, apiResult{Data: input})
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
