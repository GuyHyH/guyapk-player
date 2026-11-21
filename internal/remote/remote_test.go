package remote

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeHost struct{}

func (fakeHost) RemoteStatus() Status      { return Status{Phase: "ready"} }
func (fakeHost) RemoteApps() []AppInfo     { return []AppInfo{{Package: "th.demo.app", Label: "Demo"}} }
func (fakeHost) RemoteStart() error        { return nil }
func (fakeHost) RemoteLaunch(string) error { return nil }
func (fakeHost) RemoteRotate() (bool, error) {
	return true, nil
}
func (fakeHost) RemoteKey(string) error { return nil }

type fakeStream struct {
	ch      chan Packet
	mu      sync.Mutex
	touches [][3]uint16
	keys    int
}

func (f *fakeStream) Packets() <-chan Packet { return f.ch }
func (f *fakeStream) Touch(a, _ uint8, x, y uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches = append(f.touches, [3]uint16{uint16(a), x, y})
	return nil
}
func (f *fakeStream) RequestKey() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys++
	return nil
}
func (f *fakeStream) Close() error { return nil }

type fakeSource struct{ s *fakeStream }

func (f fakeSource) Open(context.Context) (Stream, error) { return f.s, nil }

func TestParseHeader(t *testing.T) {
	var h [12]byte
	binary.BigEndian.PutUint32(h[:], 1<<31)
	binary.BigEndian.PutUint32(h[4:], 1200)
	binary.BigEndian.PutUint32(h[8:], 1920)
	if p, _, err := parseHeader(h); err != nil || p.Kind != KindSession || p.W != 1200 || p.H != 1920 {
		t.Fatalf("session: %+v %v", p, err)
	}
	binary.BigEndian.PutUint64(h[:], 1<<62)
	binary.BigEndian.PutUint32(h[8:], 30)
	if p, n, err := parseHeader(h); err != nil || p.Kind != KindConfig || n != 30 {
		t.Fatalf("config: %+v %d %v", p, n, err)
	}
	binary.BigEndian.PutUint64(h[:], 1<<61|12345)
	if p, _, err := parseHeader(h); err != nil || p.Kind != KindKey || p.PTS != 12345 {
		t.Fatalf("key: %+v %v", p, err)
	}
	binary.BigEndian.PutUint64(h[:], 777)
	if p, _, err := parseHeader(h); err != nil || p.Kind != KindDelta || p.PTS != 777 {
		t.Fatalf("delta: %+v %v", p, err)
	}
	binary.BigEndian.PutUint32(h[8:], maxPacket+1)
	if _, _, err := parseHeader(h); err == nil {
		t.Fatal("oversized packet accepted")
	}
}

func TestTouchMessage(t *testing.T) {
	m := touchMessage(touchDown, 3, 65535, 0, 1200, 1920)
	if len(m) != 32 || m[0] != ctlTouch || m[1] != touchDown {
		t.Fatalf("header: %v", m[:2])
	}
	if binary.BigEndian.Uint64(m[2:]) != 3 || binary.BigEndian.Uint32(m[10:]) != 1199 || binary.BigEndian.Uint32(m[14:]) != 0 {
		t.Fatalf("position: %v", m)
	}
	if binary.BigEndian.Uint16(m[18:]) != 1200 || binary.BigEndian.Uint16(m[20:]) != 1920 || binary.BigEndian.Uint16(m[22:]) != 0xffff {
		t.Fatalf("size or pressure: %v", m)
	}
	if up := touchMessage(touchUp, 0, 1, 1, 100, 100); binary.BigEndian.Uint16(up[22:]) != 0 {
		t.Fatal("touch up must have zero pressure")
	}
}

func TestCertificateChain(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := loadOrCreateCA(dir)
	if err != nil || !again.cert.Equal(ca.cert) {
		t.Fatalf("CA must persist: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	for _, c := range []struct {
		ip string
		ok bool
	}{{"192.168.1.20", true}, {"10.5.5.5", true}, {"172.20.0.9", true}, {"8.8.8.8", false}} {
		leaf, err := ca.issue(net.ParseIP(c.ip))
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(leaf.Certificate[0])
		_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: c.ip, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		if (err == nil) != c.ok {
			t.Errorf("%s: verify err=%v, want ok=%v", c.ip, err, c.ok)
		}
		if days := cert.NotAfter.Sub(cert.NotBefore).Hours() / 24; days > 398 {
			t.Errorf("leaf valid for %.0f days, Apple allows 398", days)
		}
	}
	// A public name must not chain to this CA, even if someone signs one.
	sneaky := *ca.cert
	if len(sneaky.PermittedIPRanges) != 3 || !sneaky.PermittedDNSDomainsCritical {
		t.Fatal("CA is missing its name constraints")
	}
	p := ca.mobileconfig()
	for _, want := range []string{"com.apple.security.root", "<data>", "PayloadUUID"} {
		if !bytes.Contains(p, []byte(want)) {
			t.Errorf("profile missing %s", want)
		}
	}
}

func startTest(t *testing.T) (*Server, *fakeStream, Info) {
	t.Helper()
	fs := &fakeStream{ch: make(chan Packet, 8)}
	s := New(Options{Dir: t.TempDir(), Host: fakeHost{}, Source: fakeSource{fs}, Addr: net.IPv4(127, 0, 0, 1), Plain: true})
	info, err := s.Enable()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Disable)
	return s, fs, info
}

func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{}}}
}

func post(c *http.Client, base, path, body string) (*http.Response, error) {
	u := strings.TrimSuffix(base, "/")
	req, _ := http.NewRequest(http.MethodPost, u+path, strings.NewReader(body))
	req.Header.Set("Origin", u)
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}

func status(t *testing.T, res *http.Response, err error) int {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func TestPairingAndAuth(t *testing.T) {
	s, _, info := startTest(t)
	c := client(t)

	res, err := c.Get(info.AppURL + "api/apps")
	if got := status(t, res, err); got != http.StatusUnauthorized {
		t.Fatalf("apps without pairing = %d", got)
	}
	res, err = post(c, info.AppURL, "/api/rotate", "")
	if got := status(t, res, err); got != http.StatusUnauthorized {
		t.Fatalf("rotate without pairing = %d", got)
	}
	wrong := "000000"
	if info.PIN == wrong {
		wrong = "000001"
	}
	res, err = post(c, info.AppURL, "/api/pair", `{"pin":"`+wrong+`"}`)
	if got := status(t, res, err); got != http.StatusUnauthorized {
		t.Fatalf("wrong pin = %d", got)
	}
	// A cross-site request must be refused even with the right PIN.
	req, _ := http.NewRequest(http.MethodPost, info.AppURL+"api/pair", strings.NewReader(`{"pin":"`+info.PIN+`"}`))
	req.Header.Set("Origin", "https://evil.example")
	res, err = c.Do(req)
	if got := status(t, res, err); got != http.StatusForbidden {
		t.Fatalf("cross-origin pair = %d", got)
	}
	res, err = post(c, info.AppURL, "/api/pair", `{"pin":"`+info.PIN+`"}`)
	if got := status(t, res, err); got != http.StatusOK {
		t.Fatalf("pair = %d", got)
	}
	res, err = c.Get(info.AppURL + "api/apps")
	if got := status(t, res, err); got != http.StatusOK {
		t.Fatalf("apps after pairing = %d", got)
	}
	// A foreign Host header is how DNS rebinding reaches a LAN service.
	req, _ = http.NewRequest(http.MethodGet, info.AppURL+"api/me", nil)
	req.Host = "evil.example"
	res, err = c.Do(req)
	if got := status(t, res, err); got != http.StatusMisdirectedRequest {
		t.Fatalf("foreign host = %d", got)
	}
	if _, err := s.UnpairAll(); err != nil {
		t.Fatal(err)
	}
	res, err = c.Get(info.AppURL + "api/apps")
	if got := status(t, res, err); got != http.StatusUnauthorized {
		t.Fatalf("apps after unpair = %d", got)
	}
}

func TestPINLockout(t *testing.T) {
	s, _, info := startTest(t)
	bad := "000000"
	if info.PIN == bad {
		bad = "000001"
	}
	var last string
	for i := 0; i < maxFailures; i++ {
		_, last = s.tryPair(bad)
	}
	if last != "locked" {
		t.Fatalf("last failure = %q", last)
	}
	if _, code := s.tryPair(info.PIN); code != "locked" {
		t.Fatalf("correct old PIN during lock = %q", code)
	}
	if s.Info().PIN == info.PIN {
		t.Fatal("PIN must change after lockout")
	}
}

func TestSetupPortExposesOnlySetup(t *testing.T) {
	_, _, info := startTest(t)
	c := client(t)
	for _, p := range []string{"api/apps", "ws", "app.js", "api/pair"} {
		res, err := c.Get(info.SetupURL + p)
		if got := status(t, res, err); got != http.StatusNotFound {
			t.Errorf("setup port /%s = %d", p, got)
		}
	}
}

func TestWebSocketStream(t *testing.T) {
	_, fs, info := startTest(t)
	c := client(t)
	res, err := post(c, info.AppURL, "/api/pair", `{"pin":"`+info.PIN+`"}`)
	if got := status(t, res, err); got != http.StatusOK {
		t.Fatalf("pair = %d", got)
	}
	wsURL := "ws" + strings.TrimPrefix(info.AppURL, "http") + "ws"
	head := http.Header{"Origin": {strings.TrimSuffix(info.AppURL, "/")}}
	if _, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": head["Origin"]}); err == nil {
		t.Fatal("WebSocket opened without a session")
	}
	u, _ := http.NewRequest(http.MethodGet, info.AppURL, nil)
	for _, ck := range c.Jar.Cookies(u.URL) {
		head.Add("Cookie", ck.Name+"="+ck.Value)
	}
	bad := http.Header{"Origin": {"https://evil.example"}, "Cookie": head["Cookie"]}
	if _, _, err := websocket.DefaultDialer.Dial(wsURL, bad); err == nil {
		t.Fatal("WebSocket opened from another origin")
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, head)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	kind, msg, err := conn.ReadMessage()
	if err != nil || kind != websocket.TextMessage {
		t.Fatalf("state: %v", err)
	}
	var st map[string]any
	json.Unmarshal(msg, &st)
	if st["t"] != "state" || st["phase"] != "ready" {
		t.Fatalf("state = %s", msg)
	}
	fs.ch <- Packet{Kind: KindSession, W: 800, H: 1280}
	fs.ch <- Packet{Kind: KindConfig, Data: []byte{0, 0, 0, 1, 0x67}}
	fs.ch <- Packet{Kind: KindKey, PTS: 42, Data: []byte{0, 0, 0, 1, 0x65}}
	for _, want := range []Kind{KindSession, KindConfig, KindKey} {
		_, m, err := conn.ReadMessage()
		if err != nil || Kind(m[0]) != want {
			t.Fatalf("packet %d: %v %v", want, m, err)
		}
	}
	touch := []byte{touchDown, 0, 0x80, 0, 0x40, 0}
	if err := conn.WriteMessage(websocket.BinaryMessage, touch); err != nil {
		t.Fatal(err)
	}
	conn.WriteMessage(websocket.BinaryMessage, []byte{9, 0, 0, 0, 0, 0}) // invalid action
	conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"key"}`))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fs.mu.Lock()
		done := len(fs.touches) == 1 && fs.keys == 1
		fs.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("touches=%v keys=%d", fs.touches, fs.keys)
}
