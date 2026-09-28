package buddy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A subscriber retains at most one invalidation. State is always read from the
// database, so coalescing or missing a notification never requires event replay.
type changes struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

func (c *changes) subscribe() (chan struct{}, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subscribers == nil {
		c.subscribers = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	c.subscribers[ch] = struct{}{}
	return ch, func() { c.mu.Lock(); delete(c.subscribers, ch); c.mu.Unlock() }
}
func (c *changes) publish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ch := range c.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (s *Store) changed(err error) error {
	if err == nil {
		s.changes.publish()
	}
	return err
}

func serveEvents(rw http.ResponseWriter, r *http.Request, c *changes, instance string) {
	ch, unsubscribe := c.subscribe()
	defer unsubscribe()
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(rw)
	send := func(event string) error {
		// Override the server's response-wide deadline only for this stream. Each
		// write remains bounded, including when a client stops reading.
		if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, instance); err != nil {
			return err
		}
		if err := controller.Flush(); err != nil {
			return err
		}
		// An idle HTTP/2 stream must not expire between heartbeats. Bound only the
		// actual write, then remove the deadline while awaiting the next event.
		return controller.SetWriteDeadline(time.Time{})
	}
	if send("change") != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if send("change") != nil {
				return
			}
		case <-heartbeat.C:
			if send("heartbeat") != nil {
				return
			}
		}
	}
}

type liveResponse struct {
	http.ResponseWriter
	request *http.Request
}
type liveRegion struct {
	ID      string `json:"id"`
	HTML    string `json:"html"`
	Version string `json:"version"`
	Append  bool   `json:"append,omitempty"`
}
type livePage struct {
	Instance string       `json:"instance"`
	CSRF     string       `json:"csrf"`
	Title    string       `json:"title"`
	Regions  []liveRegion `json:"regions"`
	Catalog  any          `json:"catalog,omitempty"`
}

func regionVersion(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}
func (w *Web) live(rw http.ResponseWriter, r *http.Request) {
	rw = &liveResponse{rw, r}
	switch r.URL.Query().Get("view") {
	case "dashboard":
		w.dashboard(rw, r)
	case "session":
		r.SetPathValue("id", r.URL.Query().Get("id"))
		w.session(rw, r)
	default:
		http.Error(rw, "Unknown live view", http.StatusBadRequest)
	}
}

// Existing form endpoints continue serving HTML redirects. Enhanced forms ask
// for JSON so failures and successes can be displayed without losing drafts.
type actionResponse struct {
	http.ResponseWriter
	written bool
	discard bool
}

func (rw *actionResponse) WriteHeader(status int) {
	if rw.written {
		return
	}
	rw.written = true
	if status == http.StatusSeeOther {
		rw.discard = true
		location := rw.Header().Get("Location")
		rw.Header().Del("Location")
		rw.Header().Set("Content-Type", "application/json")
		rw.ResponseWriter.WriteHeader(http.StatusOK)
		json.NewEncoder(rw.ResponseWriter).Encode(map[string]string{"location": location})
		return
	}
	rw.ResponseWriter.WriteHeader(status)
}
func (rw *actionResponse) Write(b []byte) (int, error) {
	if rw.discard {
		return len(b), nil
	}
	rw.written = true
	return rw.ResponseWriter.Write(b)
}
func jsonActions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && (strings.HasPrefix(r.URL.Path, "/sessions/") || r.URL.Path == "/spools/refresh") && strings.Contains(r.Header.Get("Accept"), "application/json") {
			rw = &actionResponse{ResponseWriter: rw}
		}
		next.ServeHTTP(rw, r)
	})
}
func renderJSONError(rw http.ResponseWriter, status int, message string) {
	if a, ok := rw.(*actionResponse); ok {
		rw = a.ResponseWriter
		a.written = true
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(map[string]string{"error": message})
}

func (w *Web) renderLive(rw *liveResponse, p Page) {
	known := map[string]string{}
	// Region hashes are only bandwidth hints; they do not authorize mutations.
	_ = json.Unmarshal([]byte(rw.request.URL.Query().Get("known")), &known)
	result := livePage{Instance: w.csrf, CSRF: w.csrf, Title: p.Title, Regions: []liveRegion{}}
	names := []string{"library-list", "pagination", "printers"}
	if p.View == "session" {
		names = []string{"session-heading", "summary", "frames", "video-controls", "jobs", "manage"}
	}
	if p.View == "session" && rw.request.URL.Query().Get("audit") == "true" {
		names = append(names, "provenance")
	}
	for _, name := range names {
		var b bytes.Buffer
		if err := w.templates.ExecuteTemplate(&b, name, p); err != nil {
			renderJSONError(rw, 500, "Unable to update this view")
			return
		}
		version := regionVersion(b.Bytes())
		if name != "frames" && known[name] == version {
			continue
		}
		result.Regions = append(result.Regions, liveRegion{ID: name, HTML: b.String(), Version: version, Append: name == "frames"})
	}

	writeJSON(rw, result)
}
