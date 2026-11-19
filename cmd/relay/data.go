package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type relayFrame struct {
	Type    string              `json:"type"`
	ID      string              `json:"id,omitempty"`
	Method  string              `json:"method,omitempty"`
	Path    string              `json:"path,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Status  int                 `json:"status,omitempty"`
	Data    string              `json:"data,omitempty"`
	Error   string              `json:"error,omitempty"`
}

type relayPending struct {
	frames chan relayFrame
	done   chan struct{}
	once   sync.Once
}

func (p *relayPending) finish() { p.once.Do(func() { close(p.done) }) }
func (p *relayPending) deliver(f relayFrame) bool {
	select {
	case p.frames <- f:
		return true
	case <-p.done:
		return false
	}
}

type relayNode struct {
	id             string
	ws             *websocket.Conn
	writeMu        sync.Mutex
	mu             sync.Mutex
	pending        map[string]*relayPending
	bandwidth      int64
	maxConnections int
	active         int
	throttleMu     sync.Mutex
	nextWrite      time.Time
}

type relayDataPlane struct {
	s        *server
	mu       sync.RWMutex
	nodes    map[string]*relayNode
	upgrader websocket.Upgrader
}

func newRelayDataPlane(s *server) *relayDataPlane {
	return &relayDataPlane{s: s, nodes: make(map[string]*relayNode), upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}}
}

func (d *relayDataPlane) connect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	id, token := strings.TrimSpace(r.URL.Query().Get("node_id")), strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if id == "" || token == "" || !d.authorized(r.Context(), id, token) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ws, err := d.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	n := &relayNode{id: id, ws: ws, pending: make(map[string]*relayPending), bandwidth: 10485760, maxConnections: 8}
	_ = d.s.db.QueryRowContext(r.Context(), `SELECT bandwidth_bytes,max_connections FROM relay_nodes WHERE node_id=?`, id).Scan(&n.bandwidth, &n.maxConnections)
	d.mu.Lock()
	old := d.nodes[id]
	d.nodes[id] = n
	d.mu.Unlock()
	if old != nil {
		_ = old.ws.Close()
	}
	log.Printf("[relay] node online id=%s bandwidth=%dB/s max_connections=%d", id, n.bandwidth, n.maxConnections)
	_, _ = d.s.db.ExecContext(r.Context(), `UPDATE relay_nodes SET status='online',last_seen_at=CURRENT_TIMESTAMP WHERE node_id=?`, id)
	defer func() {
		d.mu.Lock()
		current := d.nodes[id] == n
		if current {
			delete(d.nodes, id)
		}
		d.mu.Unlock()
		n.closePending()
		_ = n.ws.Close()
		if current {
			_, _ = d.s.db.ExecContext(context.Background(), `UPDATE relay_nodes SET status='approved',last_seen_at=CURRENT_TIMESTAMP WHERE node_id=? AND status!='revoked'`, id)
		}
	}()
	d.readNode(n)
}

func (d *relayDataPlane) authorized(ctx context.Context, id, token string) bool {
	var stored, status string
	if err := d.s.db.QueryRowContext(ctx, `SELECT token_hash,status FROM relay_nodes WHERE node_id=?`, id).Scan(&stored, &status); err != nil || status == "revoked" {
		return false
	}
	actual := sha(token)
	return len(stored) == len(actual) && subtle.ConstantTimeCompare([]byte(stored), []byte(actual)) == 1
}

func (d *relayDataPlane) readNode(n *relayNode) {
	for {
		var f relayFrame
		msgType, payload, err := n.ws.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.TextMessage || json.Unmarshal(payload, &f) != nil {
			continue
		}
		n.mu.Lock()
		p := n.pending[f.ID]
		n.mu.Unlock()
		if p != nil {
			if f.Type == "response_end" {
				p.deliver(f)
				n.mu.Lock()
				delete(n.pending, f.ID)
				n.mu.Unlock()
				// 不要在 response_end 入队后立刻关闭 done：done 一旦关闭，
				// forward 的 select 可能在仍有 body 帧排队时提前选中 done，导致响应被截断。
				// forward 消费到 response_end 后会通过 defer finish。
			} else {
				if !p.deliver(f) {
					continue
				}
			}
		}
	}
}

func (n *relayNode) send(f relayFrame) error {
	payload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	return n.ws.WriteMessage(websocket.TextMessage, payload)
}

func (n *relayNode) closePending() {
	n.mu.Lock()
	pending := n.pending
	n.pending = make(map[string]*relayPending)
	n.mu.Unlock()
	for _, p := range pending {
		p.finish()
	}
}

func (n *relayNode) throttle(size int) {
	if size <= 0 {
		return
	}
	n.throttleMu.Lock()
	bandwidth := n.bandwidth
	if bandwidth <= 0 {
		n.throttleMu.Unlock()
		return
	}
	now := time.Now()
	start := now
	if n.nextWrite.After(start) {
		start = n.nextWrite
	}
	delay := time.Duration(int64(time.Second) * int64(size) / bandwidth)
	n.nextWrite = start.Add(delay)
	wait := time.Until(start)
	n.throttleMu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

func (d *relayDataPlane) forwardRoot(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	var id string
	for nodeID := range d.nodes {
		if id != "" {
			d.mu.RUnlock()
			http.Error(w, "node id required", http.StatusBadRequest)
			return
		}
		id = nodeID
	}
	d.mu.RUnlock()
	if id == "" {
		http.Error(w, "node offline", http.StatusBadGateway)
		return
	}
	r.URL.Path = "/n/" + id + r.URL.Path
	d.forward(w, r)
}

func (d *relayDataPlane) find(id string) *relayNode {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.nodes[id]
}

func (d *relayDataPlane) revoke(id string) {
	d.mu.Lock()
	n := d.nodes[id]
	delete(d.nodes, id)
	d.mu.Unlock()
	if n != nil {
		_ = n.ws.Close()
		n.closePending()
	}
}

func rewriteRelayHTML(body []byte, prefix string) []byte {
	for _, attr := range []string{"href=\"/", "src=\"/", "action=\"/"} {
		body = bytes.ReplaceAll(body, []byte(attr), []byte(strings.TrimSuffix(attr, "/")+prefix+"/"))
	}
	patch := []byte(`<script>(function(){var p='` + prefix + `';function u(v){return typeof v==='string'&&v.charAt(0)==='/'&&v.indexOf(p+'/')!==0?p+v:v}var f=window.fetch;window.fetch=function(a,o){if(typeof a==='string')a=u(a);return f.call(this,a,o)};var x=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(m,a){arguments[1]=u(a);return x.apply(this,arguments)};var sa=Element.prototype.setAttribute;Element.prototype.setAttribute=function(k,v){if((k==='src'||k==='href'||k==='action')&&typeof v==='string')v=u(v);return sa.call(this,k,v)};function patchSrc(C){if(!C)return;var d=Object.getOwnPropertyDescriptor(C.prototype,'src');if(!d||!d.set)return;Object.defineProperty(C.prototype,'src',{configurable:true,get:d.get,set:function(v){return d.set.call(this,u(v))}})}patchSrc(HTMLImageElement);patchSrc(HTMLMediaElement);patchSrc(HTMLSourceElement);var op=window.open;window.open=function(a){if(typeof a==='string')a=u(a);return op.apply(this,arguments)}})();</script>`)
	if i := bytes.Index(bytes.ToLower(body), []byte("<head>")); i >= 0 {
		end := i + len("<head>")
		body = append(body[:end], append(patch, body[end:]...)...)
	} else {
		body = append(patch, body...)
	}
	return body
}

func (d *relayDataPlane) forward(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var responseBytes int64
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/n/"), "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	n := d.find(parts[0])
	if n == nil {
		http.Error(w, "node offline", http.StatusBadGateway)
		return
	}
	defer func() {
		log.Printf("[relay] %s %s node=%s bytes=%d duration=%s", r.Method, r.URL.Path, parts[0], responseBytes, time.Since(started))
	}()
	path := "/"
	if len(parts) == 2 && parts[1] != "" {
		path += parts[1]
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	n.mu.Lock()
	if n.active >= n.maxConnections {
		n.mu.Unlock()
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	n.active++
	n.mu.Unlock()
	defer func() { n.mu.Lock(); n.active--; n.mu.Unlock() }()
	id := randomToken(12)
	p := &relayPending{frames: make(chan relayFrame, 8), done: make(chan struct{})}
	n.mu.Lock()
	n.pending[id] = p
	n.mu.Unlock()
	defer func() { n.mu.Lock(); delete(n.pending, id); n.mu.Unlock(); p.finish() }()
	if err := n.send(relayFrame{Type: "request_start", ID: id, Method: r.Method, Path: path, Headers: r.Header}); err != nil {
		http.Error(w, "node unavailable", 502)
		return
	}
	go func() {
		defer r.Body.Close()
		buf := make([]byte, 48*1024)
		for {
			nr, er := r.Body.Read(buf)
			if nr > 0 {
				n.throttle(nr)
				if e := n.send(relayFrame{Type: "request_body", ID: id, Data: base64.StdEncoding.EncodeToString(buf[:nr])}); e != nil {
					p.deliver(relayFrame{Type: "response_end", ID: id, Error: e.Error()})
					p.finish()
					return
				}
			}
			if er == io.EOF {
				break
			}
			if er != nil {
				p.deliver(relayFrame{Type: "response_end", ID: id, Error: er.Error()})
				p.finish()
				return
			}
		}
		_ = n.send(relayFrame{Type: "request_end", ID: id})
	}()
	deadline := time.NewTimer(2 * time.Hour)
	defer deadline.Stop()
	var responseHeaders map[string][]string
	var responseStatus int
	var htmlMode bool
	var htmlBody bytes.Buffer
	for {
		select {
		case f, ok := <-p.frames:
			if !ok {
				return
			}
			switch f.Type {
			case "response_start":
				responseHeaders, responseStatus = f.Headers, f.Status
				for k, values := range f.Headers {
					if strings.EqualFold(k, "Location") {
						for _, v := range values {
							if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "/n/") {
								v = "/n/" + parts[0] + v
							}
							w.Header().Add(k, v)
						}
						continue
					}
					if strings.EqualFold(k, "Content-Type") {
						htmlMode = strings.Contains(strings.ToLower(strings.Join(values, ";")), "text/html")
					}
					if !htmlMode {
						for _, v := range values {
							w.Header().Add(k, v)
						}
					}
				}
				if !htmlMode {
					w.WriteHeader(f.Status)
				}
			case "response_body":
				chunk, e := base64.StdEncoding.DecodeString(f.Data)
				if e != nil {
					return
				}
				n.throttle(len(chunk))
				responseBytes += int64(len(chunk))
				if htmlMode {
					htmlBody.Write(chunk)
				} else {
					if _, e = w.Write(chunk); e != nil {
						return
					}
					_ = http.NewResponseController(w).Flush()
				}
			case "response_end":
				if f.Error != "" {
					return
				}
				if htmlMode {
					for k, values := range responseHeaders {
						for _, v := range values {
							if strings.EqualFold(k, "Location") && strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "/n/") {
								v = "/n/" + parts[0] + v
							}
							if !strings.EqualFold(k, "Content-Length") {
								w.Header().Add(k, v)
							}
						}
					}
					body := rewriteRelayHTML(htmlBody.Bytes(), "/n/"+parts[0])
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
					w.WriteHeader(responseStatus)
					_, _ = w.Write(body)
				}
				return
			}
		case <-deadline.C:
			http.Error(w, "relay request timeout", 504)
			return
		case <-r.Context().Done():
			return
		case <-p.done:
			return
		}
	}
}
