package buddy

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestVideoFilename(t *testing.T) {
	for name, want := range map[string]string{
		"Workshop parts tray": "Workshop parts tray.mp4", "模型 café": "模型 café.mp4",
		"../folder\\print\r\n": "_folder_print__.mp4", "...": "Untitled session.mp4",
		"CON": "_CON.mp4", "lpt1.test": "_lpt1.test.mp4", "name. ": "name.mp4",
	} {
		if got := videoFilename(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
	long := videoFilename(strings.Repeat("模型", 100))
	if !utf8.ValidString(long) || len(long) > 185 {
		t.Fatal("filename must be bounded, valid UTF-8")
	}
}

func TestVideoDownloadIntent(t *testing.T) {
	s, _ := testService(t)
	if s.Config.Video.DefaultDurationSeconds != 10 {
		t.Fatal("default should be 10 seconds")
	}
	now := time.Now()
	handle(t, s, "START core-one Workshop parts tray", now)
	handle(t, s, "LAYER core-one 1", now)
	id := active(t, s, "core-one").ID
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		// Browsers never send URL fragments in HTTP requests.
		path, _, _ = strings.Cut(path, "#")
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	page := request("GET", "/sessions/"+id, nil).Body.String()
	if !strings.Contains(page, `value="10" required`) {
		t.Fatal("default not rendered")
	}
	csrf := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(page)[1]
	form := url.Values{"csrf": {csrf}, "duration": {"10"}}
	w := request("POST", "/sessions/"+id+"/videos", form)
	if w.Code != 303 {
		t.Fatalf("create: %d", w.Code)
	}
	location, _ := url.Parse(w.Header().Get("Location"))
	jobID := location.Query().Get("download")
	j, err := s.Store.Job(jobID)
	if err != nil || j.SessionID != id {
		t.Fatalf("redirect must identify requested job: %v", err)
	}
	if !strings.Contains(request("GET", location.String(), nil).Body.String(), `data-auto-download="`+jobID+`"`) {
		t.Fatal("missing download intent")
	}
	if err := s.Root.MkdirAll(filepath.Dir(jobPath(j)), 0750); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(s.Root, jobPath(j), []byte("test-video")); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.FinishJob(j.ID, "ready", ""); err != nil {
		t.Fatal(err)
	}
	if again := request("POST", "/sessions/"+id+"/videos", form); again.Header().Get("Location") != location.String() {
		t.Fatal("cached video must be a new automatic download intent")
	}
	w = request("GET", "/videos/"+jobID+"/download", nil)
	_, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || params["filename"] != "Workshop parts tray.mp4" || w.Body.String() != "test-video" {
		t.Fatalf("download: %d %v", w.Code, params)
	}
	if err := s.Store.SetDisplayName(id, "Renamed / parts tray", ""); err != nil {
		t.Fatal(err)
	}
	if again := request("POST", "/sessions/"+id+"/videos", form); again.Header().Get("Location") != location.String() {
		t.Fatal("rename invalidated a cached video")
	}
	w = request("GET", "/videos/"+jobID+"/download", nil)
	_, params, err = mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || params["filename"] != "Renamed _ parts tray.mp4" || w.Body.String() != "test-video" {
		t.Fatalf("renamed download: %d %v", w.Code, params)
	}
	handle(t, s, "START mini Another print", now)
	other := active(t, s, "mini").ID
	for _, path := range []string{"/sessions/" + id, "/sessions/" + other + "?download=" + jobID, "/sessions/" + id + "?download=unknown"} {
		if !strings.Contains(request("GET", path, nil).Body.String(), `data-auto-download=""`) {
			t.Fatal("unrelated visits must not download")
		}
	}
}

const videoOffer = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=recvonly\r\n"

func TestWebRTCSignaling(t *testing.T) {
	s, _ := testService(t)
	s.Config.AuthUser, s.Config.AuthPassword = "user", "secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/api/webrtc" || r.URL.Query().Get("src") != "camera" || len(r.URL.Query()) != 1 {
			t.Error("unconfigured stream or endpoint")
		}
		if u, p, _ := r.BasicAuth(); u != "upstream" || p != "password" {
			t.Error("wrong upstream credentials")
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("browser cookie leaked")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != videoOffer || r.Header.Get("Content-Type") != "application/sdp" {
			t.Error("wrong SDP request")
		}
		w.WriteHeader(201)
		io.WriteString(w, "v=0\r\nanswer")
	}))
	defer upstream.Close()
	s.Config.Go2RTC.URL = strings.Replace(upstream.URL, "://", "://upstream:password@", 1) + "/prefix"
	h, _ := NewWeb(s)
	page := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("user", "secret")
	h.ServeHTTP(page, r)
	body := page.Body.String()
	if strings.Contains(body, "password@") || !strings.Contains(body, "data-live=") || strings.Contains(body, "Latest saved frame") {
		t.Fatal("incorrect live card")
	}
	csrf := regexp.MustCompile(`data-csrf="([a-f0-9]+)"`).FindStringSubmatch(body)[1]
	request := func(id, offer, token string, auth bool, ctx context.Context) *httptest.ResponseRecorder {
		form := url.Values{"csrf": {token}, "sdp": {offer}, "src": {"untrusted"}}
		r := httptest.NewRequest("POST", "/api/printers/"+id+"/webrtc", strings.NewReader(form.Encode())).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Cookie", "private=secret")
		if auth {
			r.SetBasicAuth("user", "secret")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		id, offer, token string
		auth             bool
		want             int
	}{
		{"core-one", videoOffer, csrf, true, 200}, {"missing", videoOffer, csrf, true, 404},
		{"core-one", videoOffer, "", true, 403}, {"core-one", videoOffer, csrf, false, 401},
		{"core-one", strings.ReplaceAll(videoOffer, "recvonly", "sendrecv"), csrf, true, 400},
		{"core-one", videoOffer + "m=audio 9 RTP/AVP 0\r\n", csrf, true, 400},
		{"core-one", videoOffer + "m=application 9 DTLS/SCTP 5000\r\n", csrf, true, 400},
		{"core-one", videoOffer + "\na=sendonly\n", csrf, true, 400},
		{"core-one", strings.Repeat("x", 70<<10), csrf, true, 400},
	} {
		if w := request(tc.id, tc.offer, tc.token, tc.auth, context.Background()); w.Code != tc.want {
			t.Errorf("%s: got %d want %d", tc.id, w.Code, tc.want)
		}
	}
	for _, kind := range []string{"error", "invalid", "oversized", "redirect", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				switch kind {
				case "error":
					http.Error(w, "sensitive camera detail", 500)
				case "invalid":
					io.WriteString(w, "<html>not SDP</html>")
				case "oversized":
					io.WriteString(w, "v=0\r\n"+strings.Repeat("x", maxSDP))
				case "redirect":
					w.Header().Set("Location", upstream.URL)
					w.WriteHeader(302)
				case "timeout":
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			s.Config.Go2RTC.URL = server.URL
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			w := request("core-one", videoOffer, csrf, true, ctx)
			if w.Code != 502 || strings.Contains(w.Body.String(), "sensitive") {
				t.Fatalf("unsafe upstream response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
