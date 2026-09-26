package buddy

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxSDP = 48 << 10

// Proxy only signaling for configured streams. Media flows directly between
// the browser and go2rtc; internal addresses and credentials stay server-side.
var signalingClient = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (w *Web) webrtc(rw http.ResponseWriter, r *http.Request) {
	stream := ""
	for _, p := range w.service.Config.Printers {
		if p.ID == r.PathValue("id") {
			stream = p.Stream
			break
		}
	}
	if stream == "" {
		http.Error(rw, "Printer not found", http.StatusNotFound)
		return
	}
	offer := r.PostForm.Get("sdp")
	// One receive-only video section: no microphone, publishing, or data channel.
	if !validVideoOffer(offer) {
		http.Error(rw, "Expected a receive-only video offer", http.StatusBadRequest)
		return
	}
	u, _ := url.Parse(w.service.Config.Go2RTC.URL)
	u.Path = strings.TrimRight(u.Path, "/") + "/api/webrtc"
	u.RawQuery = url.Values{"src": {stream}}.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, u.String(), strings.NewReader(offer))
	if err != nil {
		http.Error(rw, "Live preview unavailable", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/sdp")
	res, err := signalingClient.Do(req)
	if err != nil {
		http.Error(rw, "Cannot connect to go2rtc", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxSDP+1))
	if err != nil || len(body) > maxSDP || res.StatusCode < 200 || res.StatusCode >= 300 || !strings.HasPrefix(string(body), "v=0\r\n") {
		http.Error(rw, "go2rtc could not start this stream", http.StatusBadGateway)
		return
	}
	rw.Header().Set("Content-Type", "application/sdp")
	rw.Write(body)
}

func validVideoOffer(sdp string) bool {
	if len(sdp) > maxSDP || !strings.HasPrefix(sdp, "v=0\r\n") || strings.ContainsAny(strings.ReplaceAll(sdp, "\r\n", ""), "\r\n\x00") {
		return false
	}
	media, receive := 0, 0
	for _, line := range strings.Split(sdp, "\r\n") {
		if strings.HasPrefix(line, "m=") {
			media++
			if !strings.HasPrefix(line, "m=video ") {
				return false
			}
		}
		switch line {
		case "a=recvonly":
			receive++
		case "a=sendonly", "a=sendrecv", "a=inactive":
			return false
		}
	}
	return media == 1 && receive == 1
}
