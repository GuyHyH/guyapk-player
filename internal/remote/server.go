// Package remote lets an iPad use GuyAPK over the local network as an installed
// web app. Android keeps running on the computer; the iPad shows the screen and
// sends touches over an encrypted, PIN-paired connection.
package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web
var webFS embed.FS

const (
	cookieName  = "guyapk_remote"
	pinLifetime = 10 * time.Minute
	maxFailures = 5
	lockTime    = time.Minute
	maxTokens   = 8
	basePort    = 8780

	touchDown   = 0
	touchUp     = 1
	touchMove   = 2
	touchCancel = 3
)

// Status is the device state shown on the iPad.
type Status struct {
	Phase     string `json:"phase"`
	Step      string `json:"step"`
	Landscape bool   `json:"landscape"`
}

// AppInfo is one entry of the iPad's app list.
type AppInfo struct {
	Package string `json:"package"`
	Label   string `json:"label"`
	Icon    string `json:"icon"`
}

// Host is the part of GuyAPK the iPad can control.
type Host interface {
	RemoteStatus() Status
	RemoteApps() []AppInfo
	RemoteStart() error
	RemoteLaunch(pkg string) error
	RemoteRotate() (bool, error)
	RemoteKey(name string) error
}

// Options configure a Server.
type Options struct {
	Dir    string
	Host   Host
	Source Source
	// Addr overrides LAN detection. It is for automated tests.
	Addr net.IP
	// Plain serves the app without TLS on loopback. It is for automated tests.
	Plain bool
	// OnChange is called when pairing or connection state changes.
	OnChange func()
}

// Info is the status shown in GuyAPK on the computer.
type Info struct {
	Enabled     bool   `json:"enabled"`
	IP          string `json:"ip"`
	SetupURL    string `json:"setupUrl"`
	AppURL      string `json:"appUrl"`
	PIN         string `json:"pin"`
	PINExpires  int64  `json:"pinExpires"`
	QR          string `json:"qr"`
	Connected   bool   `json:"connected"`
	Paired      int    `json:"paired"`
	Fingerprint string `json:"fingerprint"`
}

// Server is the iPad remote service. It listens only while enabled.
type Server struct {
	opt Options

	mu       sync.Mutex
	enabled  bool
	ip       net.IP
	subnet   *net.IPNet
	httpPort int
	tlsPort  int
	plain    *http.Server
	secure   *http.Server
	ca       *authority
	qr       string
	pin      string
	pinUntil time.Time
	failures int
	lockedTo time.Time
	tokens   []tokenEntry
	client   *websocket.Conn
}

type tokenEntry struct {
	Hash    string `json:"hash"`
	Created int64  `json:"created"`
}

// New creates a stopped server.
func New(opt Options) *Server {
	s := &Server{opt: opt}
	s.loadTokens()
	return s
}

func (s *Server) loadTokens() {
	b, err := os.ReadFile(filepath.Join(s.opt.Dir, "tokens.json"))
	if err == nil {
		json.Unmarshal(b, &s.tokens)
	}
}

func (s *Server) saveTokens() error {
	if err := os.MkdirAll(s.opt.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s.tokens)
	if err != nil {
		return err
	}
	p := filepath.Join(s.opt.Dir, "tokens.json")
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

func (s *Server) changed() {
	if s.opt.OnChange != nil {
		go s.opt.OnChange()
	}
}

// primaryLAN finds the private address of the adapter used for the default
// route. Connecting a UDP socket selects a route without sending any packet.
func primaryLAN() (net.IP, *net.IPNet, error) {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return nil, nil, errors.New("remote:no_lan")
	}
	ip := c.LocalAddr().(*net.UDPAddr).IP.To4()
	c.Close()
	if ip == nil || !ip.IsPrivate() {
		return nil, nil, errors.New("remote:no_lan")
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, nil, errors.New("remote:no_lan")
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return ip, &net.IPNet{IP: ip.Mask(n.Mask), Mask: n.Mask}, nil
		}
	}
	return nil, nil, errors.New("remote:no_lan")
}

func randomPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// Enable starts listening on the private LAN address only.
func (s *Server) Enable() (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled {
		return s.infoLocked(), nil
	}
	var (
		ip     net.IP
		subnet *net.IPNet
		err    error
	)
	if s.opt.Addr != nil {
		ip = s.opt.Addr
		subnet = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
		if !ip.IsLoopback() {
			subnet = &net.IPNet{IP: ip.Mask(net.CIDRMask(8, 32)), Mask: net.CIDRMask(8, 32)}
		}
	} else if ip, subnet, err = primaryLAN(); err != nil {
		return Info{}, err
	}

	var cert tls.Certificate
	if !s.opt.Plain {
		if s.ca, err = loadOrCreateCA(s.opt.Dir); err != nil {
			return Info{}, fmt.Errorf("remote:cert: %w", err)
		}
		if cert, err = s.ca.issue(ip); err != nil {
			return Info{}, fmt.Errorf("remote:cert: %w", err)
		}
	}

	var hl, sl net.Listener
	for p := basePort; p < basePort+20; p += 2 {
		if hl, err = net.Listen("tcp4", net.JoinHostPort(ip.String(), strconv.Itoa(p))); err != nil {
			continue
		}
		if sl, err = net.Listen("tcp4", net.JoinHostPort(ip.String(), strconv.Itoa(p+1))); err != nil {
			hl.Close()
			continue
		}
		s.httpPort, s.tlsPort = p, p+1
		break
	}
	if hl == nil || sl == nil || err != nil {
		return Info{}, errors.New("remote:port")
	}

	s.ip, s.subnet = ip, subnet
	if s.pin, err = randomPIN(); err != nil {
		hl.Close()
		sl.Close()
		return Info{}, err
	}
	s.pinUntil = time.Now().Add(pinLifetime)
	s.failures, s.lockedTo = 0, time.Time{}

	s.plain = &http.Server{Handler: s.setupHandler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	s.secure = &http.Server{
		Handler:           s.appHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// HTTP/2 cannot carry the WebSocket upgrade used for video.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	if s.opt.Plain {
		go s.secure.Serve(sl)
	} else {
		s.secure.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
		go s.secure.ServeTLS(sl, "", "")
	}
	go s.plain.Serve(hl)

	url := s.setupURL()
	png, err := qrcode.Encode(url, qrcode.Medium, 512)
	if err != nil {
		s.stopLocked()
		return Info{}, err
	}
	s.qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	s.enabled = true
	return s.infoLocked(), nil
}

func (s *Server) setupURL() string {
	return "http://" + net.JoinHostPort(s.ip.String(), strconv.Itoa(s.httpPort)) + "/"
}

func (s *Server) appURL() string {
	scheme := "https"
	if s.opt.Plain {
		scheme = "http"
	}
	return scheme + "://" + net.JoinHostPort(s.ip.String(), strconv.Itoa(s.tlsPort)) + "/"
}

func (s *Server) stopLocked() {
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	for _, h := range []*http.Server{s.plain, s.secure} {
		if h != nil {
			h.Close()
		}
	}
	s.plain, s.secure = nil, nil
	s.enabled = false
	s.pin = ""
}

// Disable stops listening and disconnects the iPad.
func (s *Server) Disable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// Info reports current status, renewing an expired PIN.
func (s *Server) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled && time.Now().After(s.pinUntil) {
		s.renewPINLocked()
	}
	return s.infoLocked()
}

func (s *Server) infoLocked() Info {
	i := Info{Enabled: s.enabled, Paired: len(s.tokens), Connected: s.client != nil}
	if s.enabled {
		i.IP, i.SetupURL, i.AppURL, i.PIN, i.PINExpires, i.QR = s.ip.String(), s.setupURL(), s.appURL(), s.pin, s.pinUntil.Unix(), s.qr
		if s.ca != nil {
			i.Fingerprint = s.ca.fingerprint()
		}
	}
	return i
}

func (s *Server) renewPINLocked() {
	if p, err := randomPIN(); err == nil {
		s.pin = p
		s.pinUntil = time.Now().Add(pinLifetime)
		s.failures = 0
	}
}

// NewPIN replaces the pairing PIN.
func (s *Server) NewPIN() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled {
		s.renewPINLocked()
	}
	return s.infoLocked()
}

// UnpairAll revokes every paired iPad and disconnects the current one.
func (s *Server) UnpairAll() (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = nil
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	return s.infoLocked(), s.saveTokens()
}

// tryPair checks a PIN and issues a session token.
func (s *Server) tryPair(pin string) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Before(s.lockedTo) {
		return "", "locked"
	}
	if s.pin == "" || now.After(s.pinUntil) {
		return "", "expired"
	}
	if subtle.ConstantTimeCompare([]byte(pin), []byte(s.pin)) != 1 {
		s.failures++
		if s.failures >= maxFailures {
			s.lockedTo = now.Add(lockTime)
			s.renewPINLocked()
			s.changed()
			return "", "locked"
		}
		return "", "wrong"
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "error"
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.tokens = append(s.tokens, tokenEntry{hashToken(token), now.Unix()})
	if len(s.tokens) > maxTokens {
		s.tokens = s.tokens[len(s.tokens)-maxTokens:]
	}
	s.failures = 0
	if err := s.saveTokens(); err != nil {
		return "", "error"
	}
	s.changed()
	return token, ""
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || len(c.Value) < 32 {
		return false
	}
	want := hashToken(c.Value)
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := 0
	for _, t := range s.tokens {
		ok |= subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want))
	}
	return ok == 1
}

// guard rejects anything outside the local subnet, and foreign Host headers,
// which blocks DNS-rebinding attacks from websites the iPad might visit.
func (s *Server) guard(port func() int, csp func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		client := net.ParseIP(host)
		s.mu.Lock()
		subnet, ip := s.subnet, s.ip
		s.mu.Unlock()
		if err != nil || client == nil || subnet == nil || (!subnet.Contains(client) && !client.IsLoopback()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Host != net.JoinHostPort(ip.String(), strconv.Itoa(port())) {
			http.Error(w, "bad host", http.StatusMisdirectedRequest)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", csp(r))
		next.ServeHTTP(w, r)
	})
}

func staticFile(w http.ResponseWriter, name string) {
	b, err := fs.ReadFile(webFS, "web/"+name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	types := map[string]string{
		".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8", ".png": "image/png", ".webmanifest": "application/manifest+json",
	}
	w.Header().Set("Content-Type", types[path.Ext(name)])
	w.Write(b)
}

// setupHandler is the plain-HTTP page that installs the certificate. It exposes
// nothing else, and never sees the PIN or the screen.
func (s *Server) setupHandler() http.Handler {
	allowed := map[string]string{"/": "setup.html", "/setup.js": "setup.js", "/style.css": "style.css", "/icon-180.png": "icon-180.png"}
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/ca.mobileconfig" {
			s.mu.Lock()
			ca := s.ca
			s.mu.Unlock()
			if ca == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/x-apple-aspen-config")
			w.Header().Set("Content-Disposition", `attachment; filename="GuyAPK-Remote.mobileconfig"`)
			w.Write(ca.mobileconfig())
			return
		}
		if r.URL.Path == "/setup.json" {
			writeJSON(w, map[string]string{"app": s.Info().AppURL})
			return
		}
		f, ok := allowed[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		staticFile(w, f)
	})
	return s.guard(func() int { return s.httpPort }, func(*http.Request) string {
		app := strings.TrimSuffix(s.Info().AppURL, "/")
		return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self' " + app + "; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	}, mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return o == "https://"+r.Host || o == "http://"+r.Host
}

func (s *Server) appHandler() http.Handler {
	mux := http.NewServeMux()
	for route, file := range map[string]string{
		"/app.js": "app.js", "/style.css": "style.css", "/manifest.webmanifest": "manifest.webmanifest",
		"/icon-180.png": "icon-180.png", "/icon-512.png": "icon-512.png",
	} {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, _ *http.Request) { staticFile(w, file) })
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) { staticFile(w, "index.html") })
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"paired": s.authed(r)})
	})
	mux.HandleFunc("POST /api/pair", s.handlePair)
	mux.Handle("GET /api/state", s.requireAuth(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.opt.Host.RemoteStatus()) }))
	mux.Handle("GET /api/apps", s.requireAuth(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.opt.Host.RemoteApps()) }))
	mux.Handle("POST /api/start", s.requireAction(func(_ map[string]string) (any, error) {
		// Booting takes a while; the iPad follows progress over the WebSocket.
		go s.opt.Host.RemoteStart()
		return map[string]bool{"ok": true}, nil
	}))
	mux.Handle("POST /api/launch", s.requireAction(func(b map[string]string) (any, error) {
		pkg := b["package"]
		found := false
		for _, a := range s.opt.Host.RemoteApps() {
			found = found || a.Package == pkg
		}
		if !found {
			return nil, errors.New("unknown_app")
		}
		go s.opt.Host.RemoteLaunch(pkg)
		return map[string]bool{"ok": true}, nil
	}))
	mux.Handle("POST /api/rotate", s.requireAction(func(_ map[string]string) (any, error) {
		l, err := s.opt.Host.RemoteRotate()
		return map[string]bool{"landscape": l}, err
	}))
	mux.Handle("POST /api/key", s.requireAction(func(b map[string]string) (any, error) {
		return map[string]bool{"ok": true}, s.opt.Host.RemoteKey(b["name"])
	}))
	mux.HandleFunc("GET /ws", s.handleWS)

	return s.guard(func() int { return s.tlsPort }, func(r *http.Request) string {
		scheme := "wss://"
		if s.opt.Plain {
			scheme = "ws://"
		}
		return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; manifest-src 'self'; connect-src 'self' " + scheme + r.Host + "; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	}, mux)
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "origin", http.StatusForbidden)
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token, code := s.tryPair(strings.TrimSpace(body.PIN))
	if code != "" {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": code})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", MaxAge: 365 * 24 * 3600,
		HttpOnly: true, Secure: !s.opt.Plain, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, map[string]bool{"paired": true})
}

func (s *Server) requireAuth(f http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f(w, r)
	})
}

func (s *Server) requireAction(f func(map[string]string) (any, error)) http.Handler {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "origin", http.StatusForbidden)
			return
		}
		body := map[string]string{}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		res, err := f(body)
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, res)
	})
}

// EncodePacket builds the WebSocket message for a packet.
func EncodePacket(p Packet) []byte {
	switch p.Kind {
	case KindSession:
		m := make([]byte, 5)
		m[0] = byte(KindSession)
		binary.BigEndian.PutUint16(m[1:], uint16(p.W))
		binary.BigEndian.PutUint16(m[3:], uint16(p.H))
		return m
	case KindConfig:
		return append([]byte{byte(KindConfig)}, p.Data...)
	}
	m := make([]byte, 9+len(p.Data))
	m[0] = byte(p.Kind)
	binary.BigEndian.PutUint64(m[1:], p.PTS)
	copy(m[9:], p.Data)
	return m
}

type wsOut struct {
	kind int
	data []byte
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	up := websocket.Upgrader{
		ReadBufferSize: 1024, WriteBufferSize: 1 << 16,
		CheckOrigin: sameOrigin,
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(1024)

	// One iPad at a time: the newest connection takes over.
	s.mu.Lock()
	if s.client != nil {
		s.client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "replaced"), time.Now().Add(time.Second))
		s.client.Close()
	}
	s.client = conn
	s.mu.Unlock()
	s.changed()

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan wsOut, 12)
	var cur struct {
		sync.Mutex
		st Stream
	}
	defer func() {
		cancel()
		conn.Close()
		s.mu.Lock()
		if s.client == conn {
			s.client = nil
		}
		s.mu.Unlock()
		s.changed()
	}()

	go func() { // writer
		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ping.C:
				if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					cancel()
					return
				}
			case m := <-out:
				conn.SetWriteDeadline(time.Now().Add(6 * time.Second))
				if conn.WriteMessage(m.kind, m.data) != nil {
					cancel()
					return
				}
			}
		}
	}()

	go func() { // reader: touches and key-frame requests
		defer cancel()
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(45 * time.Second)) })
		for {
			kind, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			conn.SetReadDeadline(time.Now().Add(45 * time.Second))
			cur.Lock()
			st := cur.st
			cur.Unlock()
			if st == nil {
				continue
			}
			switch {
			case kind == websocket.BinaryMessage && len(msg) == 6 && msg[0] <= touchCancel && msg[1] < 10:
				st.Touch(msg[0], msg[1], binary.BigEndian.Uint16(msg[2:]), binary.BigEndian.Uint16(msg[4:]))
			case kind == websocket.TextMessage && string(msg) == `{"t":"key"}`:
				st.RequestKey()
			}
		}
	}()

	sendState := func(extra string) bool {
		b, _ := json.Marshal(struct {
			T string `json:"t"`
			Status
			Error string `json:"error,omitempty"`
		}{"state", s.opt.Host.RemoteStatus(), extra})
		select {
		case out <- wsOut{websocket.TextMessage, b}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	wait := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-ctx.Done():
			return false
		}
	}

	for ctx.Err() == nil {
		if !sendState("") {
			return
		}
		if s.opt.Host.RemoteStatus().Phase != "ready" {
			if !wait(time.Second) {
				return
			}
			continue
		}
		st, err := s.opt.Source.Open(ctx)
		if err != nil {
			if !sendState("stream") || !wait(3*time.Second) {
				return
			}
			continue
		}
		cur.Lock()
		cur.st = st
		cur.Unlock()
		s.pump(ctx, st, out)
		cur.Lock()
		cur.st = nil
		cur.Unlock()
		st.Close()
		if !wait(400 * time.Millisecond) {
			return
		}
	}
}

// pump forwards video. When Wi-Fi cannot keep up it drops frames up to the next
// key frame, because low latency matters more than showing every frame.
func (s *Server) pump(ctx context.Context, st Stream, out chan<- wsOut) {
	skipping := false
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-st.Packets():
			if !ok {
				return
			}
			msg := wsOut{websocket.BinaryMessage, EncodePacket(p)}
			switch p.Kind {
			case KindSession, KindConfig:
				skipping = false
				select {
				case out <- msg:
				case <-ctx.Done():
					return
				}
				continue
			case KindKey:
				skipping = false
			case KindDelta:
				if skipping {
					continue
				}
			}
			select {
			case out <- msg:
			default:
				if !skipping {
					skipping = true
					st.RequestKey()
				}
			}
		}
	}
}
