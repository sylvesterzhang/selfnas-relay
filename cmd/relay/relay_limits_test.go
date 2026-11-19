package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	_ "modernc.org/sqlite"
)

func testRelayDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE relay_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE relay_nodes (node_id TEXT PRIMARY KEY, token_hash TEXT NOT NULL DEFAULT '', device_name TEXT NOT NULL DEFAULT '', hostname TEXT NOT NULL DEFAULT '', platform TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, bandwidth_bytes INTEGER NOT NULL DEFAULT 10485760, max_connections INTEGER NOT NULL DEFAULT 8, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, last_seen_at TEXT, revoked_at TEXT)`,
		`INSERT INTO relay_nodes(node_id,device_name,status,bandwidth_bytes,max_connections,created_at,last_seen_at) VALUES('node-test','test-device','online',10485760,8,'2026-11-19 10:00:00','2026-11-19 10:01:00')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNodesReturnsSQLiteTextTimestamps(t *testing.T) {
	s := &server{db: testRelayDB(t)}
	r := httptest.NewRequest("GET", "/api/nodes", nil)
	w := httptest.NewRecorder()
	s.nodes(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || result.Data[0]["node_id"] != "node-test" {
		t.Fatalf("expected existing node in list; got %s", w.Body.String())
	}
}

func TestLimitUpdateAppliesToPersistedAndOnlineNodes(t *testing.T) {
	db := testRelayDB(t)
	conn, serverConn := websocketConnPair(t)
	defer conn.Close()
	defer serverConn.Close()
	node := &relayNode{id: "node-test", ws: serverConn, pending: make(map[string]*relayPending), bandwidth: 10485760, maxConnections: 8}
	s := &server{db: db, data: &relayDataPlane{nodes: map[string]*relayNode{"node-test": node}}}
	body := `{"default_bandwidth_bytes":"131072","max_connections":"3"}`
	r := httptest.NewRequest("PUT", "/api/settings/limits", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.limits(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var bandwidth int64
	var maxConnections int
	if err := db.QueryRow(`SELECT bandwidth_bytes,max_connections FROM relay_nodes WHERE node_id='node-test'`).Scan(&bandwidth, &maxConnections); err != nil {
		t.Fatal(err)
	}
	if bandwidth != 131072 || maxConnections != 3 {
		t.Fatalf("persisted limits = %d B/s, %d connections", bandwidth, maxConnections)
	}
	node.throttleMu.Lock()
	gotBandwidth := node.bandwidth
	node.throttleMu.Unlock()
	node.mu.Lock()
	gotConnections := node.maxConnections
	node.mu.Unlock()
	if gotBandwidth != 131072 || gotConnections != 3 {
		t.Fatalf("online limits = %d B/s, %d connections", gotBandwidth, gotConnections)
	}
}

func TestThrottleEnforcesConfiguredRate(t *testing.T) {
	n := &relayNode{bandwidth: 1024}
	start := time.Now()
	n.throttle(128)
	n.throttle(128)
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("two 128-byte chunks at 1KiB/s took only %s", elapsed)
	}
}

func websocketConnPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	serverConnCh := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			serverConnCh <- conn
		}
	}))
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case serverConn := <-serverConnCh:
		return clientConn, serverConn
	case <-time.After(2 * time.Second):
		clientConn.Close()
		t.Fatal("timed out waiting for websocket peer")
		return nil, nil
	}
}
