package remote

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"guyapk/internal/android"
)

const (
	scrcpyVersion = "5.0.1"
	scrcpyURL     = "https://github.com/Genymobile/scrcpy/releases/download/v5.0.1/scrcpy-server-v5.0.1"
	scrcpySHA256  = "764eb6f79811d5211fe9df341120882ba9994c7a61b897d7bf3fb662e53bc536"
	deviceJar     = "/data/local/tmp/guyapk-scrcpy.jar"
	codecH264     = 0x68323634
	maxPacket     = 16 << 20

	ctlTouch      = 2
	ctlResetVideo = 17
)

// Kind identifies a packet in the video stream.
type Kind byte

// Packet kinds, which are also the first byte of each WebSocket message.
const (
	KindSession Kind = 0
	KindConfig  Kind = 1
	KindKey     Kind = 2
	KindDelta   Kind = 3
)

// Packet is one unit of the encoded video stream.
type Packet struct {
	Kind Kind
	PTS  uint64
	W, H int
	Data []byte
}

// Stream is a live screen and input connection.
type Stream interface {
	Packets() <-chan Packet
	Touch(action, pointer uint8, nx, ny uint16) error
	RequestKey() error
	Close() error
}

// Source opens screen streams.
type Source interface {
	Open(ctx context.Context) (Stream, error)
}

// Scrcpy streams from a device with the open-source scrcpy server. The server
// is fetched once on first use, so it adds nothing to the installer size.
type Scrcpy struct {
	Device   android.Device
	CacheDir string
	MaxSize  int
	BitRate  int
	LogFile  string // optional server log, for diagnostics
}

func (s *Scrcpy) server(ctx context.Context) (string, error) {
	file := filepath.Join(s.CacheDir, "scrcpy-server-v"+scrcpyVersion)
	if ok, _ := hasHash(file, scrcpySHA256); ok {
		return file, nil
	}
	if err := os.MkdirAll(s.CacheDir, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scrcpyURL, nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("remote:download: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("remote:download: HTTP %d", res.StatusCode)
	}
	tmp := file + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, io.LimitReader(res.Body, 16<<20))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("remote:download: %w", err)
	}
	if ok, err := hasHash(tmp, scrcpySHA256); err != nil || !ok {
		os.Remove(tmp)
		return "", errors.New("remote:download: checksum mismatch")
	}
	return file, os.Rename(tmp, file)
}

func hasHash(file, want string) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}

// Open starts the server on the device and connects to it.
func (s *Scrcpy) Open(ctx context.Context) (Stream, error) {
	jar, err := s.server(ctx)
	if err != nil {
		return nil, err
	}
	d := s.Device
	if _, err := d.Run(ctx, 30*time.Second, "push", jar, deviceJar); err != nil {
		return nil, fmt.Errorf("remote:push: %w", err)
	}
	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	scid := binary.BigEndian.Uint32(id[:]) & 0x7fffffff
	name := fmt.Sprintf("scrcpy_%08x", scid)
	out, err := d.Run(ctx, 10*time.Second, "forward", "tcp:0", "localabstract:"+name)
	if err != nil {
		return nil, fmt.Errorf("remote:forward: %w", err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return nil, fmt.Errorf("remote:forward: %q", out)
	}

	maxSize, bitRate := s.MaxSize, s.BitRate
	if maxSize == 0 {
		maxSize = 1600
	}
	if bitRate == 0 {
		bitRate = 8_000_000
	}
	pctx, cancel := context.WithCancel(ctx)
	st := &scrcpyStream{device: d, port: port, cancel: cancel, packets: make(chan Packet, 32), done: pctx.Done()}
	cmd := d.Command(pctx, "shell", "CLASSPATH="+deviceJar, "app_process", "/",
		"com.genymobile.scrcpy.Server", scrcpyVersion,
		fmt.Sprintf("scid=%08x", scid), "log_level=warn", "tunnel_forward=true",
		"audio=false", "control=true", "video_codec=h264",
		"max_size="+strconv.Itoa(maxSize), "video_bit_rate="+strconv.Itoa(bitRate), "max_fps=60",
		"send_device_meta=false", "send_dummy_byte=true", "send_stream_meta=true", "send_frame_meta=true",
		"clipboard_autosync=false", "power_on=false", "cleanup=true", "downsize_on_error=true")
	if s.LogFile != "" {
		if f, err := os.Create(s.LogFile); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close()
		}
	}
	if err := cmd.Start(); err != nil {
		st.Close()
		return nil, fmt.Errorf("remote:start: %w", err)
	}
	st.proc = cmd
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()

	if st.video, err = dialServer(pctx, port, true, exited); err != nil {
		st.Close()
		return nil, fmt.Errorf("remote:connect: %w", err)
	}
	if st.control, err = dialServer(pctx, port, false, exited); err != nil {
		st.Close()
		return nil, fmt.Errorf("remote:connect: %w", err)
	}
	go st.read()
	go st.drainControl()
	return st, nil
}

// dialServer retries until the device side listens. adb accepts the local
// connection even when nothing is listening, so the first socket must also
// receive scrcpy's dummy byte before it counts as connected.
func dialServer(ctx context.Context, port int, first bool, exited <-chan struct{}) (net.Conn, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-exited:
			return nil, errors.New("server exited")
		default:
		}
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			if !first {
				return c, nil
			}
			var b [1]byte
			c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
			if _, err = io.ReadFull(c, b[:]); err == nil {
				c.SetReadDeadline(time.Time{})
				return c, nil
			}
			c.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, errors.New("timed out")
}

type scrcpyStream struct {
	device  android.Device
	port    int
	cancel  context.CancelFunc
	proc    *exec.Cmd
	video   net.Conn
	control net.Conn
	packets chan Packet
	done    <-chan struct{}

	mu   sync.Mutex // guards control writes and the current size
	w, h int
	once sync.Once
}

func (s *scrcpyStream) Packets() <-chan Packet { return s.packets }

func (s *scrcpyStream) read() {
	defer close(s.packets)
	defer s.Close()
	r := bufio.NewReaderSize(s.video, 1<<18)
	var head [12]byte
	if _, err := io.ReadFull(r, head[:4]); err != nil {
		return
	}
	if binary.BigEndian.Uint32(head[:4]) != codecH264 {
		return
	}
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return
		}
		p, size, err := parseHeader(head)
		if err != nil {
			return
		}
		if p.Kind == KindSession {
			s.mu.Lock()
			s.w, s.h = p.W, p.H
			s.mu.Unlock()
		} else {
			p.Data = make([]byte, size)
			if _, err := io.ReadFull(r, p.Data); err != nil {
				return
			}
		}
		select {
		case s.packets <- p:
		case <-s.done:
			return
		}
	}
}

// parseHeader decodes scrcpy's 12-byte session or frame header.
func parseHeader(h [12]byte) (Packet, int, error) {
	v := binary.BigEndian.Uint64(h[:8])
	if v>>63 == 1 {
		w, ht := binary.BigEndian.Uint32(h[4:8]), binary.BigEndian.Uint32(h[8:12])
		if w == 0 || ht == 0 || w > 16384 || ht > 16384 {
			return Packet{}, 0, errors.New("bad session size")
		}
		return Packet{Kind: KindSession, W: int(w), H: int(ht)}, 0, nil
	}
	size := binary.BigEndian.Uint32(h[8:12])
	if size == 0 || size > maxPacket {
		return Packet{}, 0, errors.New("bad packet size")
	}
	p := Packet{Kind: KindDelta, PTS: v & (1<<61 - 1)}
	switch {
	case v&(1<<62) != 0:
		p.Kind, p.PTS = KindConfig, 0
	case v&(1<<61) != 0:
		p.Kind = KindKey
	}
	return p, int(size), nil
}

// drainControl discards device messages so the socket never fills up.
func (s *scrcpyStream) drainControl() { io.Copy(io.Discard, s.control) }

// touchMessage encodes an inject-touch control message. nx and ny are the
// position across the screen from 0 to 65535.
func touchMessage(action, pointer uint8, nx, ny uint16, w, h int) []byte {
	m := make([]byte, 32)
	m[0], m[1] = ctlTouch, action
	binary.BigEndian.PutUint64(m[2:], uint64(pointer))
	binary.BigEndian.PutUint32(m[10:], uint32(int(nx)*(w-1)/65535))
	binary.BigEndian.PutUint32(m[14:], uint32(int(ny)*(h-1)/65535))
	binary.BigEndian.PutUint16(m[18:], uint16(w))
	binary.BigEndian.PutUint16(m[20:], uint16(h))
	if action != touchUp {
		binary.BigEndian.PutUint16(m[22:], 0xffff)
	}
	return m
}

func (s *scrcpyStream) Touch(action, pointer uint8, nx, ny uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == 0 || s.control == nil {
		return nil
	}
	s.control.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := s.control.Write(touchMessage(action, pointer, nx, ny, s.w, s.h))
	return err
}

func (s *scrcpyStream) RequestKey() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.control == nil {
		return nil
	}
	s.control.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := s.control.Write([]byte{ctlResetVideo})
	return err
}

func (s *scrcpyStream) Close() error {
	s.once.Do(func() {
		if s.video != nil {
			s.video.Close()
		}
		if s.control != nil {
			s.control.Close()
		}
		s.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.device.Run(ctx, 4*time.Second, "forward", "--remove", "tcp:"+strconv.Itoa(s.port))
	})
	return nil
}
