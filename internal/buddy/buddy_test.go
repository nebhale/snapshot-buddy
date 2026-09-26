package buddy

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	c, err := DecodeConfig(strings.NewReader(`metrics:
  advertised_host: 127.0.0.1
go2rtc:
  url: http://localhost:1984
printers:
  - id: core-one
    stream: camera
  - id: mini
    stream: other
`))
	if err != nil {
		t.Fatal(err)
	}
	c.DataDir = t.TempDir()
	return c
}

func testJPEG(t *testing.T, w, h int, c color.Color) Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return Image{b.Bytes(), w, h}
}

type fakeCamera struct {
	mu    sync.Mutex
	image Image
	err   error
	calls int
}

func (f *fakeCamera) Capture(context.Context, string) (Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.image, f.err
}

func testService(t *testing.T) (*Service, *fakeCamera) {
	t.Helper()
	c := testConfig(t)
	store, err := OpenStore(c.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	cam := &fakeCamera{image: testJPEG(t, 64, 48, color.RGBA{R: 220, A: 255})}
	s, err := NewService(c, store, cam)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Root.Close(); s.Store.Close() })
	return s, cam
}

func marker(t *testing.T, payload string, at time.Time) Event {
	t.Helper()
	e, err := ParseMarker(markerPrefix + payload)
	if err != nil {
		t.Fatal(err)
	}
	e.SourceIP = "127.0.0.1"
	e.ReceivedAt = at.UTC()
	return e
}

func handle(t *testing.T, s *Service, payload string, at time.Time) {
	t.Helper()
	if err := s.Handle(context.Background(), marker(t, payload, at)); err != nil {
		t.Fatal(err)
	}
}

func active(t *testing.T, s *Service, p string) Session {
	t.Helper()
	v, err := s.Store.Active(p)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestConfiguration(t *testing.T) {
	valid := `metrics: {advertised_host: "192.168.1.50"}
go2rtc: {url: "http://go2rtc:1984"}
printers: [{id: core-one, stream: camera}]`
	for _, tc := range []struct{ name, config string }{
		{"unknown field", valid + "\nunknown: 1"},
		{"duplicate ID", strings.Replace(valid, "stream: camera}]", "stream: camera}, {id: core-one, stream: other}]", 1)},
		{"bad ID", strings.Replace(valid, "core-one", "../../outside", 1)},
		{"ID exceeds firmware buffer", strings.Replace(valid, "core-one", "123456789012345678901234", 1)},
		{"source IP", strings.Replace(valid, "stream: camera", "stream: camera, source_ip: nope", 1)},
		{"stream URL", strings.Replace(valid, "stream: camera", "stream: 'rtsp://camera/live'", 1)},
		{"bad scheme", strings.Replace(valid, "http://go2rtc:1984", "file:///tmp/camera", 1)},
		{"query cache", strings.Replace(valid, "http://go2rtc:1984", "http://go2rtc:1984?cache=1m", 1)},
		{"empty printers", strings.Replace(valid, "[{id: core-one, stream: camera}]", "[]", 1)},
		{"empty data dir", valid + "\ndata_dir: ''"},
		{"invalid port", valid + "\nhttp: {address: ':0'}"},
		{"bad timeout", valid + "\ncapture: {timeout: 0s}"},
		{"zero workers", valid + "\nvideo: {workers: 0}"},
		{"multiple documents", valid + "\n---\n{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeConfig(strings.NewReader(tc.config)); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	c, err := DecodeConfig(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Address != ":8080" || c.Capture.Timeout != 5*time.Second || c.Printers[0].Name != "core-one" {
		t.Fatalf("wrong defaults: %+v", c)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SNAPSHOT_BUDDY_AUTH_USER", "buddy")
	t.Setenv("SNAPSHOT_BUDDY_AUTH_PASSWORD", "")
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("accepted partial auth")
	}
	t.Setenv("SNAPSHOT_BUDDY_AUTH_PASSWORD", "secret")
	if c, err := LoadConfig(p); err != nil || c.AuthPassword != "secret" {
		t.Fatalf("auth load: %v", err)
	}
}

func TestPacketParser(t *testing.T) {
	packet := `<14>1 2026-09-25T01:00:00Z printer buddy - - - gcode v="M118 SB1 START core-one Gearbox cover" 42
gcode v="M118 SNAPSHOT_BUDDY_V1 FRAME core-one 12" 43
temp v=210 43
gcode v="G1 X0 Y0" 44
gcode v="M118 SB1 STOP core-one 20" 45
M118 SB1 LAYER mini 8`
	events := ParsePacket([]byte(packet), "127.0.0.1", time.Now())
	if len(events) != 4 || events[0].Name != "Gearbox cover" || events[1].Kind != "LAYER" || events[1].Layer != 12 || events[3].PrinterID != "mini" {
		t.Fatalf("events: %+v", events)
	}
	for _, bad := range []string{"M118 BUDDY_TIMELAPSE_LAYER:1", markerPrefix + "FRAME core-one 1", legacyMarkerPrefix + "LAYER core-one 1", markerPrefix + "LAYER ../x 3", markerPrefix + "LAYER core-one -1", markerPrefix + "LAYER core-one 2junk", markerPrefix + "LAYER core-one 9999999999999999", markerPrefix + "LAYER core-one 1\x00", markerPrefix + "UNKNOWN core-one x", markerPrefix + "START core-one ", markerPrefix + "START core-one " + strings.Repeat("x", 1024)} {
		if _, err := ParseMarker(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if got := ParsePacket([]byte(`log message="M118 SB1 LAYER core-one 1"`), "127.0.0.1", time.Now()); len(got) != 0 {
		t.Fatal("matched unrelated metric")
	}
}

func TestFirmwareMetricLimit(t *testing.T) {
	const capacity = 47
	const longestID = "12345678901234567890123"
	for _, kind := range []string{"LAYER", "STOP"} {
		command := markerPrefix + kind + " " + longestID + " 10000000"
		if len(command) > capacity {
			t.Fatalf("%s marker exceeds firmware buffer: %d", kind, len(command))
		}
		if kind == "LAYER" && len(command) != capacity {
			t.Fatalf("LAYER marker should use the full firmware buffer: %d", len(command))
		}
		events := ParsePacket([]byte(`gcode v="`+command+`" 123`), "127.0.0.1", time.Now())
		if len(events) != 1 || events[0].Layer != 10000000 {
			t.Fatalf("lost layer: %+v", events)
		}
	}
	command := (markerPrefix + "START " + longestID + " Gearbox cover")[:capacity]
	events := ParsePacket([]byte(`gcode v="`+command+`" 123`), "127.0.0.1", time.Now())
	if len(events) != 1 || events[0].Name != "Gearbox" {
		t.Fatalf("lost truncated name: %+v", events)
	}
}

func TestDeletionDoesNotQueueBehindExport(t *testing.T) {
	s, _ := testService(t)
	handle(t, s, "LAYER core-one 1", time.Now())
	id := active(t, s, "core-one").ID
	if err := s.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	s.filesMu.RLock()
	err := s.DeleteSession(id)
	s.filesMu.RUnlock()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("delete during export: %v", err)
	}
	if err := s.DeleteSession(id); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, cam := testService(t)
	now := time.Now()
	handle(t, s, "STOP core-one 1", now)
	if list, _ := s.Store.Sessions("", 100, 0); len(list) != 0 {
		t.Fatal("orphan STOP opened session")
	}
	handle(t, s, "START core-one ../../name <script>", now)
	handle(t, s, "START core-one ../../name <script>", now.Add(100*time.Millisecond))
	first := active(t, s, "core-one")
	if first.Recovered {
		t.Fatal("named session marked recovered")
	}
	handle(t, s, "LAYER core-one 12", now)
	handle(t, s, "LAYER core-one 12", now.Add(100*time.Millisecond))
	handle(t, s, "LAYER core-one 13", now.Add(200*time.Millisecond))
	handle(t, s, "LAYER core-one 12", now.Add(3*time.Second)) // out of order duplicate
	handle(t, s, "LAYER mini 99", now)
	mini := active(t, s, "mini")
	if !mini.Recovered || mini.FirstLayer == nil || *mini.FirstLayer != 99 || mini.OpenedBy != "layer" {
		t.Fatalf("recovered: %+v", mini)
	}
	handle(t, s, "STOP core-one 14", now.Add(4*time.Second))
	handle(t, s, "STOP core-one 14", now.Add(4100*time.Millisecond))
	m, err := s.Store.Snapshot(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Session.CloseReason != "completed" || m.Session.FrameCount != 3 || len(m.Captures) != 3 || cam.calls != 4 {
		t.Fatalf("manifest %+v, calls %d", m, cam.calls)
	}
	if _, err := s.Root.Stat(filepath.Join("sessions", first.ID, m.Captures[0].File)); err != nil {
		t.Fatal(err)
	}
	handle(t, s, "START mini Real model", now.Add(5*time.Second))
	m, _ = s.Store.Snapshot(mini.ID)
	if m.Session.CloseReason != "superseded" || !m.Session.Recovered {
		t.Fatal("recovered session was not preserved separately")
	}
}

func TestManualCloseAndRestart(t *testing.T) {
	s, _ := testService(t)
	now := time.Now()
	handle(t, s, "LAYER core-one 5", now)
	id := active(t, s, "core-one").ID
	if err := s.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	handle(t, s, "LAYER core-one 6", now.Add(time.Second))
	if _, err := s.Store.Active("core-one"); !errors.Is(err, ErrNotFound) {
		t.Fatal("manual close did not suppress LAYER")
	}
	handle(t, s, "START core-one next", now.Add(3*time.Second))
	id = active(t, s, "core-one").ID
	handle(t, s, "LAYER core-one 7", now.Add(4*time.Second))
	if a := active(t, s, "core-one"); a.FrameCount != 1 {
		t.Fatal("START did not clear suppression")
	}
	if err := s.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	s.Root.Close()
	s.Store.Close()
	var err error
	s.Store, err = OpenStore(s.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := NewService(s.Config, s.Store, s.camera)
	if err != nil {
		t.Fatal(err)
	}
	s.Root = resumed.Root
	handle(t, resumed, "LAYER core-one 8", now.Add(5*time.Second))
	if !active(t, resumed, "core-one").Recovered {
		t.Fatal("restart did not clear suppression")
	}
}

func TestRestartKeepsActiveAndFailsPendingCapture(t *testing.T) {
	s, _ := testService(t)
	now := time.Now()
	handle(t, s, "LAYER core-one 1", now)
	id := active(t, s, "core-one").ID
	_, _, err := s.Store.Apply(context.Background(), marker(t, "LAYER core-one 2", now.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(s.Config.DataDir); err == nil {
		t.Fatal("allowed two instances in same data directory")
	}
	s.Root.Close()
	s.Store.Close()
	s.Store, err = OpenStore(s.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.Root, err = os.OpenRoot(s.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	ss := active(t, s, "core-one")
	if ss.ID != id || ss.FailureCount != 1 || ss.PendingCount != 0 || ss.FrameCount != 1 {
		t.Fatalf("restart: %+v", ss)
	}
	handle(t, s, "LAYER core-one 2", now.Add(4*time.Second))
	if a := active(t, s, "core-one"); a.FrameCount != 1 {
		t.Fatal("replayed missed frame at a later time")
	}
	handle(t, s, "LAYER core-one 3", now.Add(5*time.Second))
	if a := active(t, s, "core-one"); a.FrameCount != 2 {
		t.Fatal("did not continue active session")
	}
}

func TestStoreMigratesLegacyFrameKinds(t *testing.T) {
	s, _ := testService(t)
	handle(t, s, "LAYER core-one 1", time.Now())
	id := active(t, s, "core-one").ID
	if _, err := s.Store.db.Exec(`
UPDATE captures SET kind='FRAME';
UPDATE recent_events SET kind='FRAME';
UPDATE sessions SET opened_by='frame';
PRAGMA user_version=1;
`); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(s.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = store
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	m, err := store.Snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	var recentKind string
	if err := store.db.QueryRow(`SELECT kind FROM recent_events WHERE printer_id='core-one'`).Scan(&recentKind); err != nil {
		t.Fatal(err)
	}
	if version != 2 || m.Session.OpenedBy != "layer" || len(m.Captures) != 1 || m.Captures[0].Kind != "LAYER" || recentKind != "LAYER" {
		t.Fatalf("migration failed: version=%d manifest=%+v recent=%s", version, m, recentKind)
	}
}

func TestFailedAndExpiredCapture(t *testing.T) {
	s, cam := testService(t)
	cam.err = errors.New("camera offline")
	handle(t, s, "LAYER core-one 1", time.Now())
	ss := active(t, s, "core-one")
	if ss.FailureCount != 1 || ss.FrameCount != 0 {
		t.Fatalf("failure %+v", ss)
	}
	cam.err = nil
	handle(t, s, "LAYER core-one 2", time.Now().Add(-time.Minute))
	if cam.calls != 1 {
		t.Fatal("captured an expired marker")
	}
	handle(t, s, "STOP core-one 3", time.Now())
	m, _ := s.Store.Snapshot(ss.ID)
	if m.Session.State != "closed" || m.Session.FrameCount != 1 {
		t.Fatalf("completion %+v", m.Session)
	}
}

func TestCameraValidationAndRetries(t *testing.T) {
	jpegData := testJPEG(t, 24, 20, color.White).Bytes
	for _, tc := range []struct {
		name   string
		status int
		mime   string
		body   []byte
		length string
		valid  bool
	}{
		{"valid", 200, "image/jpeg", jpegData, "", true},
		{"wrong type", 200, "text/html", jpegData, "", false},
		{"bad data", 200, "image/jpeg", []byte("not jpeg"), "", false},
		{"truncated", 200, "image/jpeg", jpegData[:len(jpegData)/2], "", false},
		{"too large header", 200, "image/jpeg", jpegData, "99999999", false},
		{"too large body", 200, "image/jpeg", bytes.Repeat([]byte("x"), MaxJPEGBytes+1), "", false},
		{"upstream failure", 503, "image/jpeg", jpegData, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/base/api/frame.jpeg" || r.URL.Query().Get("src") != "camera name" || r.URL.Query().Has("cache") {
					t.Errorf("request URL %s", r.URL)
				}
				w.Header().Set("Content-Type", tc.mime)
				if tc.length != "" {
					w.Header().Set("Content-Length", tc.length)
				}
				w.WriteHeader(tc.status)
				w.Write(tc.body)
			}))
			defer upstream.Close()
			c := testConfig(t)
			c.Go2RTC.URL = upstream.URL + "/base"
			camera := NewCamera(c)
			_, err := camera.Capture(context.Background(), "camera name")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			want := int32(2)
			if tc.valid {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("calls %d", calls.Load())
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer upstream.Close()
		c := testConfig(t)
		c.Go2RTC.URL = upstream.URL
		c.Capture.Timeout = 20 * time.Millisecond
		if _, err := NewCamera(c).Capture(context.Background(), "camera"); err == nil {
			t.Fatal("no timeout")
		}
	})
	t.Run("transient", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(jpegData)
		}))
		defer upstream.Close()
		c := testConfig(t)
		c.Go2RTC.URL = upstream.URL
		if _, err := NewCamera(c).Capture(context.Background(), "camera"); err != nil {
			t.Fatal(err)
		}
	})
}

type blockingCamera struct {
	entered chan struct{}
	release chan struct{}
	image   Image
}

func (c *blockingCamera) Capture(ctx context.Context, stream string) (Image, error) {
	if stream == "camera" {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return Image{}, ctx.Err()
		case <-c.release:
		}
	}
	return c.image, nil
}

func TestUDPAndConcurrentPrinters(t *testing.T) {
	s, _ := testService(t)
	cam := &blockingCamera{entered: make(chan struct{}, 1), release: make(chan struct{}), image: testJPEG(t, 32, 32, color.White)}
	s.camera = cam
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	defer func() { cancel(); s.Wait() }()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.ServeUDP(ctx, conn) }()
	client, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.Write([]byte(`gcode v="M118 SNAPSHOT_BUDDY_V1 FRAME core-one 1" 1`))
	select {
	case <-cam.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("UDP did not reach camera")
	}
	client.Write([]byte(markerPrefix + "LAYER mini 20"))
	deadline := time.Now().Add(3 * time.Second)
	for {
		ss, err := s.Store.Active("mini")
		if err == nil && ss.FrameCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow camera blocked another printer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	statusDone := make(chan struct{})
	go func() { s.Status(); close(statusDone) }()
	select {
	case <-statusDone:
	case <-time.After(time.Second):
		t.Fatal("slow camera blocked status UI")
	}
	for i := 0; i < 70; i++ {
		s.Dispatch(marker(t, fmt.Sprintf("LAYER core-one %d", i+2), time.Now()))
	}
	if p, _ := s.Status(); p[0].Dropped == 0 {
		t.Fatal("expected bounded queue drops")
	}
	close(cam.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSourceGuardAndGCode(t *testing.T) {
	s, _ := testService(t)
	s.printers["core-one"].printer.SourceIP = "192.168.1.42"
	if s.Dispatch(marker(t, "LAYER core-one 1", time.Now())) {
		t.Fatal("accepted unexpected sender")
	}
	codes := GCode(s.Config, s.Config.Printers[0])
	for i, code := range []string{codes.Start, codes.Layer, codes.Stop} {
		copies, delay := 3, "G4 P1100\n"
		if i == 1 {
			copies, delay = 2, "G4 P100\n"
		}
		if strings.Count(code, "M118 SB1") != copies || strings.Count(code, delay) != copies-1 || strings.Count(code, "G4 ") != copies-1 {
			t.Fatalf("incorrect retry count or timing: %s", code)
		}
		var commands []string
		for _, line := range strings.Split(code, "\n") {
			if strings.HasPrefix(line, ";") {
				continue
			}
			commands = append(commands, line)
			command := strings.Fields(line)[0]
			if command != "M334" && command != "M331" && command != "M332" && command != "M118" && command != "G4" && command != "M400" {
				t.Fatalf("unexpected printer command: %s", line)
			}
		}
		if i > 0 && commands[0] != "M400" {
			t.Fatal("missing motion synchronization")
		}
	}
	for i, purpose := range []string{"start session", "layer snapshot", "final snapshot and close session"} {
		code := []string{codes.Start, codes.Layer, codes.Stop}[i]
		label := "Snapshot Buddy: " + purpose + " (core-one)"
		if !strings.HasPrefix(code, "; BEGIN "+label+"\n") || !strings.HasSuffix(code, "\n; END "+label) {
			t.Fatalf("missing identifying comments: %s", code)
		}
	}
}

func TestSetupCopiesLabeledSnippets(t *testing.T) {
	s, _ := testService(t)
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/setup", nil))
	if w.Code != 200 {
		t.Fatalf("setup response: %d", w.Code)
	}
	blocks := regexp.MustCompile(`(?s)<pre id="([^"]+)"><code>(.*?)</code></pre>`).FindAllStringSubmatch(w.Body.String(), -1)
	if len(blocks) != 3*len(s.Config.Printers) {
		t.Fatalf("got %d snippet blocks", len(blocks))
	}
	for i, p := range s.Config.Printers {
		for j, kind := range []string{"start", "stop", "layer"} {
			if blocks[i*3+j][1] != kind+"-"+p.ID {
				t.Errorf("printer %s snippets must appear in Start, End, Layer order", p.ID)
			}
		}
	}
	steps := regexp.MustCompile(`<span class="step">(\d+)</span><h3>([^<]+)</h3>`).FindAllStringSubmatch(w.Body.String(), -1)
	if len(steps) != len(blocks) {
		t.Fatal("missing numbered snippet headings")
	}
	for i, step := range steps {
		if step[1] != fmt.Sprintf("%02d", i%3+1) || step[2] != []string{"Start G-code", "End G-code", "After layer change G-code"}[i%3] {
			t.Errorf("incorrect step heading: %v", step)
		}
	}
	for _, p := range s.Config.Printers {
		codes := GCode(s.Config, p)
		for kind, expected := range map[string]string{"start": codes.Start, "layer": codes.Layer, "stop": codes.Stop} {
			found := false
			for _, block := range blocks {
				if block[1] == kind+"-"+p.ID {
					found = true
					if html.UnescapeString(block[2]) != expected {
						t.Fatalf("copyable %s snippet differs for %s", kind, p.ID)
					}
				}
			}
			if !found {
				t.Fatalf("missing %s snippet for %s", kind, p.ID)
			}
		}
	}
}

func TestWebCopyAndLibraryLayout(t *testing.T) {
	s, _ := testService(t)
	handle(t, s, "START core-one Test print", time.Now())
	id := active(t, s, "core-one").ID
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/setup", "/sessions/" + id} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, w.Code)
		}
		body := w.Body.String()
		for _, removed := range []string{"local by design", "prusaslicer", "the printer continues after the marker block", "buddy firmware limits each g-code metric", "the print journal"} {
			if strings.Contains(strings.ToLower(body), removed) {
				t.Errorf("%s still contains removed copy: %s", path, removed)
			}
		}
		if strings.Count(body, "<h1>") != 1 {
			t.Errorf("%s should have one primary heading", path)
		}
		if path == "/" {
			library := strings.Index(body, `class="library"`)
			printers := strings.Index(body, `class="printer-sidebar"`)
			if library < 0 || printers <= library {
				t.Error("library must precede printer previews in reading order")
			}
			if !strings.Contains(body, "<h1>Print library</h1>") {
				t.Error("dashboard should use a compact library heading")
			}
			if strings.Count(body, ">Printer setup<") != 1 {
				t.Error("only navigation should use the Printer setup label")
			}
			cards := regexp.MustCompile(`(?s)<article class="printer-card".*?</article>`).FindAllString(body, -1)
			if len(cards) != len(s.Config.Printers) {
				t.Fatal("missing printer cards")
			}
			for i, p := range s.Config.Printers {
				if !strings.Contains(cards[i], `href="/setup#printer-`+p.ID+`"`) {
					t.Errorf("printer %s needs a setup link inside its card", p.ID)
				}
			}
		}
		if path == "/setup" && !strings.Contains(body, "your slicer’s custom G-code fields") {
			t.Error("setup should use slicer-neutral wording")
		}
		if path == "/setup" {
			for _, p := range s.Config.Printers {
				if !strings.Contains(body, `id="printer-`+p.ID+`"`) {
					t.Errorf("missing setup anchor for %s", p.ID)
				}
			}
		}
	}
}

func TestWebLocalizableTimestamps(t *testing.T) {
	s, _ := testService(t)
	at := time.Now().UTC().Truncate(time.Second)
	handle(t, s, "LAYER core-one 42", at)
	id := active(t, s, "core-one").ID
	handle(t, s, "STOP core-one 43", at.Add(time.Second))
	before, err := s.Store.Snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/sessions/" + id} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, w.Code)
		}
		body := w.Body.String()
		for _, timestamp := range []time.Time{at, at.Add(time.Second)} {
			if !strings.Contains(body, `<time datetime="`+timestamp.Format(time.RFC3339Nano)+`" data-local-time>`) {
				t.Errorf("%s missing machine-readable time %s", path, timestamp)
			}
		}
		if !strings.Contains(body, `Recovered print — <time`) {
			t.Errorf("%s recovered name must localize its timestamp", path)
		}
		if strings.Contains(body, `datetime="0001-`) {
			t.Error("never render an uninitialized timestamp as a real date")
		}
		if path != "/" && !strings.Contains(body, "<title data-recovered-title>") {
			t.Error("recovered session page title must be localizable")
		}
	}
	after, err := s.Store.Snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Session.Name != before.Session.Name || !after.Session.OpenedAt.Equal(at) {
		t.Error("UI localization must not modify stored names or timestamps")
	}
}

func TestReadmeSnippetsMatchGenerated(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if os.IsNotExist(err) {
		t.Skip("README consistency check requires the source checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "prusaslicer") {
		t.Error("README should use slicer-neutral wording")
	}
	c := testConfig(t)
	c.Metrics.AdvertisedHost = "192.168.1.50"
	codes := GCode(c, c.Printers[0])
	blocks := regexp.MustCompile("(?s)```gcode\\n(.*?)\\n```").FindAllStringSubmatch(string(data), -1)
	if len(blocks) != 3 {
		t.Fatalf("got %d README snippets", len(blocks))
	}
	for i, expected := range []string{codes.Start, codes.Stop, codes.Layer} {
		if blocks[i][1] != expected {
			t.Fatalf("README snippet %d differs from generated G-code", i+1)
		}
	}
}

func TestWebArchiveAuthenticationAndActions(t *testing.T) {
	s, cam := testService(t)
	now := time.Now()
	handle(t, s, "START core-one <script>alert(1)</script>", now)
	handle(t, s, "LAYER core-one 1", now)
	cam.err = errors.New("camera unavailable")
	handle(t, s, "LAYER core-one 2", now)
	cam.err = nil
	handle(t, s, "LAYER core-one 3", now)
	handle(t, s, "LAYER core-one 4", now)
	id := active(t, s, "core-one").ID
	s.Config.AuthUser, s.Config.AuthPassword = "user", "password"
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, form url.Values, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		if form != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if auth {
			r.SetBasicAuth("user", "password")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/", "/setup", "/api/status", "/api/sessions/" + id, "/frames/1", "/sessions/" + id + "/archive.tar", "/videos/missing/download"} {
		if w := request("GET", path, nil, false); w.Code != 401 {
			t.Fatalf("auth %s: %d", path, w.Code)
		}
	}
	if w := request("GET", "/healthz", nil, false); w.Code != 200 {
		t.Fatalf("health %d", w.Code)
	}
	page := request("GET", "/sessions/"+id, nil, true)
	if page.Code != 200 || strings.Contains(page.Body.String(), "<script>alert") {
		t.Fatalf("page %d %s", page.Code, page.Body.String())
	}
	match := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatal("missing CSRF")
	}
	if w := request("POST", "/sessions/"+id+"/close", url.Values{}, true); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	w := request("GET", "/sessions/"+id+"/archive.tar", nil, true)
	if w.Code != 200 {
		t.Fatalf("archive %d %s", w.Code, w.Body.String())
	}
	tr := tar.NewReader(w.Body)
	head, err := tr.Next()
	if err != nil || head.Name != "manifest.json" {
		t.Fatal("manifest missing")
	}
	var m Manifest
	if err := json.NewDecoder(tr).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Session.State != "active" || m.Session.FailureCount != 1 || len(m.Captures) != 4 || m.Captures[1].Error != "camera unavailable" {
		t.Fatalf("archive lost active state or failure: %+v", m)
	}
	for i, frame := range m.Frames() {
		head, err = tr.Next()
		wantLayer := []int{1, 3, 4}[i]
		if err != nil || head.Name != fmt.Sprintf("frame_%05d.jpg", i+1) || frame.File != head.Name || frame.Layer != wantLayer {
			t.Fatalf("archive ordering at frame %d: %+v, %v", i, frame, err)
		}
		if _, err := jpeg.Decode(tr); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatal("unexpected archive entry")
	}
	form := url.Values{"csrf": {match[1]}}
	if w := request("POST", "/sessions/"+id+"/delete", form, true); w.Code != 409 {
		t.Fatal("deleted active session")
	}
	if w := request("POST", "/sessions/"+id+"/close", form, true); w.Code != 303 {
		t.Fatalf("close %d", w.Code)
	}
	if w := request("POST", "/sessions/"+id+"/delete", form, true); w.Code != 303 {
		t.Fatalf("delete %d", w.Code)
	}
	if _, err := s.Store.Session(id); !errors.Is(err, ErrNotFound) {
		t.Fatal("session survived deletion")
	}
	if _, err := s.Root.Stat(filepath.Join("sessions", id)); !os.IsNotExist(err) {
		t.Fatal("session files survived deletion")
	}
}

func TestRootRejectsSymlinkEscape(t *testing.T) {
	s, _ := testService(t)
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(s.Config.DataDir, "sessions", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(s.Root, "sessions/escape/test.jpg", []byte("x")); err == nil {
		t.Fatal("wrote outside data root")
	}
}

func FuzzParsePacket(f *testing.F) {
	f.Add([]byte(`gcode v="M118 SB1 LAYER core-one 1" 2`))
	f.Add([]byte(markerPrefix + "START core-one model"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			t.Skip()
		}
		for _, e := range ParsePacket(data, "127.0.0.1", time.Now()) {
			if len(e.Raw) > MaxMarkerBytes || !printerIDPattern.MatchString(e.PrinterID) {
				t.Fatal("invalid parsed event")
			}
		}
	})
}
