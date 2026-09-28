package buddy

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

//go:embed web/*
var assets embed.FS

type Web struct {
	service   *Service
	templates *template.Template
	csrf      string
}

type Page struct {
	Title, View, CSRF, Error string
	Printers                 []PrinterStatus
	Sessions                 []Session
	Manifest                 Manifest
	Jobs                     []Job
	Snippets                 []PrinterSnippets
	DefaultDuration          int
	Page, Previous, Next     int
	Filter                   string
	AutoDownload             string
}

type PrinterSnippets struct {
	Printer Printer
	Code    Snippets
}

func NewWeb(s *Service) (http.Handler, error) {
	t, err := template.New("pages").Funcs(template.FuncMap{
		"isoDate": func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) },
		"date": func(t time.Time) string {
			if t.IsZero() {
				return "No markers yet"
			}
			return t.UTC().Format("Jan 02, 2006 · 15:04:05 UTC")
		},
		"percent":           func(p float64) int { return int(p * 100) },
		"seconds":           func(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64) },
		"printerName":       func(id string) string { return printerDisplayName(s.Config, id) },
		"markerDescription": func(raw string) string { return markerDescription(s.Config, raw) },
		"increment":         func(n int) int { return n + 1 },
	}).ParseFS(assets, "web/*.html")
	if err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	w := &Web{service: s, templates: t, csrf: hex.EncodeToString(b)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", w.dashboard)
	mux.HandleFunc("GET /setup", w.setup)
	mux.HandleFunc("GET /sessions/{id}", w.session)
	mux.HandleFunc("POST /sessions/{id}/close", w.closeSession)
	mux.HandleFunc("POST /sessions/{id}/delete", w.deleteSession)
	mux.HandleFunc("POST /sessions/{id}/videos", w.video)
	mux.HandleFunc("GET /sessions/{id}/archive.tar", w.archive)
	mux.HandleFunc("GET /frames/{id}", w.frame)
	mux.HandleFunc("GET /videos/{id}/download", w.downloadVideo)
	mux.HandleFunc("GET /api/status", w.status)
	mux.HandleFunc("GET /api/sessions/{id}", w.sessionStatus)
	mux.HandleFunc("GET /api/jobs/{id}", w.job)
	mux.HandleFunc("POST /api/printers/{id}/webrtc", w.webrtc)
	static, _ := fs.Sub(assets, "web")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	protected := w.security(mux)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" && (r.Method == "GET" || r.Method == "HEAD") {
			if err := s.Store.db.PingContext(r.Context()); err != nil {
				http.Error(rw, "database unavailable", http.StatusServiceUnavailable)
				return
			}
			rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
			rw.Write([]byte("ok\n"))
			return
		}
		protected.ServeHTTP(rw, r)
	}), nil
}

func (w *Web) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Referrer-Policy", "same-origin")
		rw.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		rw.Header().Set("Cache-Control", "no-store")
		if w.service.Config.AuthUser != "" {
			u, p, ok := r.BasicAuth()
			uh, ph := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
			wantU, wantP := sha256.Sum256([]byte(w.service.Config.AuthUser)), sha256.Sum256([]byte(w.service.Config.AuthPassword))
			if !ok || subtle.ConstantTimeCompare(uh[:], wantU[:])&subtle.ConstantTimeCompare(ph[:], wantP[:]) != 1 {
				rw.Header().Set("WWW-Authenticate", `Basic realm="Snapshot Buddy", charset="UTF-8"`)
				http.Error(rw, "Authentication required", http.StatusUnauthorized)
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			r.Body = http.MaxBytesReader(rw, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				http.Error(rw, "Invalid form", http.StatusBadRequest)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(w.csrf)) != 1 {
				http.Error(rw, "Page expired. Reload and try again.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(rw, r)
	})
}

func (w *Web) render(rw http.ResponseWriter, status int, p Page) {
	p.CSRF = w.csrf
	p.DefaultDuration = w.service.Config.Video.DefaultDurationSeconds
	var b bytes.Buffer
	if err := w.templates.ExecuteTemplate(&b, "layout", p); err != nil {
		slog.Error("render page", "error", err)
		http.Error(rw, "Page could not be rendered", 500)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(status)
	rw.Write(b.Bytes())
}

func (w *Web) fail(rw http.ResponseWriter, err error) {
	status, message := 500, "Something went wrong. Check the service logs for details."
	switch {
	case errors.Is(err, ErrNotFound):
		status, message = 404, "That session or file could not be found."
	case errors.Is(err, ErrConflict):
		status, message = 409, "This action conflicts with active work. Before deleting, close the session and let captures, downloads, and video jobs finish. Reload the page and retry."
	default:
		slog.Error("web request", "error", err)
	}
	w.render(rw, status, Page{View: "error", Title: "Unable to complete request", Error: message})
}

func (w *Web) dashboard(rw http.ResponseWriter, r *http.Request) {
	p := Page{Title: "Print library", View: "dashboard", Filter: r.URL.Query().Get("printer")}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(0, min(page, 1000000))
	p.Page, p.Previous = page, max(0, page-1)
	var err error
	p.Printers, err = w.service.Status()
	if err != nil {
		w.fail(rw, err)
		return
	}
	p.Sessions, err = w.service.Store.Sessions(p.Filter, 25, page*24)
	if err != nil {
		w.fail(rw, err)
		return
	}
	if len(p.Sessions) > 24 {
		p.Next = page + 1
		p.Sessions = p.Sessions[:24]
	}
	w.render(rw, 200, p)
}

func (w *Web) setup(rw http.ResponseWriter, r *http.Request) {
	p := Page{Title: "Printer setup", View: "setup"}
	for _, printer := range w.service.Config.Printers {
		p.Snippets = append(p.Snippets, PrinterSnippets{printer, GCode(w.service.Config, printer)})
	}
	w.render(rw, 200, p)
}

func (w *Web) session(rw http.ResponseWriter, r *http.Request) {
	m, err := w.service.Store.Snapshot(r.PathValue("id"))
	if err != nil {
		w.fail(rw, err)
		return
	}
	jobs, err := w.service.Store.Jobs(m.Session.ID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	// Only a job belonging to this session can be an automatic download target.
	autoDownload := ""
	for _, j := range jobs {
		if j.ID == r.URL.Query().Get("download") {
			autoDownload = j.ID
		}
	}
	w.render(rw, 200, Page{Title: m.Session.Name, View: "session", Manifest: m, Jobs: jobs, AutoDownload: autoDownload})
}

func (w *Web) closeSession(rw http.ResponseWriter, r *http.Request) {
	if err := w.service.CloseSession(r.PathValue("id")); err != nil {
		w.fail(rw, err)
		return
	}
	http.Redirect(rw, r, "/sessions/"+r.PathValue("id"), http.StatusSeeOther)
}

func (w *Web) deleteSession(rw http.ResponseWriter, r *http.Request) {
	if err := w.service.DeleteSession(r.PathValue("id")); err != nil {
		w.fail(rw, err)
		return
	}
	http.Redirect(rw, r, "/", http.StatusSeeOther)
}

func (w *Web) video(rw http.ResponseWriter, r *http.Request) {
	d, err := strconv.ParseFloat(r.PostForm.Get("duration"), 64)
	if err != nil || math.IsNaN(d) || math.IsInf(d, 0) || d < 1 || d > 3600 {
		w.render(rw, 400, Page{Title: "Choose a duration", View: "error", Error: "Enter a video length between 1 and 3600 seconds."})
		return
	}
	j, err := w.service.RequestVideo(r.PathValue("id"), int64(math.Round(d*1000)))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			w.fail(rw, err)
		} else {
			w.render(rw, 400, Page{Title: "Cannot create video", View: "error", Error: err.Error()})
		}
		return
	}
	http.Redirect(rw, r, "/sessions/"+r.PathValue("id")+"?download="+j.ID+"#videos", http.StatusSeeOther)
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(rw).Encode(v); err != nil {
		slog.Debug("write JSON", "error", err)
	}
}
func (w *Web) status(rw http.ResponseWriter, r *http.Request) {
	v, err := w.service.Status()
	if err != nil {
		w.fail(rw, err)
		return
	}
	writeJSON(rw, v)
}
func (w *Web) job(rw http.ResponseWriter, r *http.Request) {
	j, err := w.service.Store.Job(r.PathValue("id"))
	if err != nil {
		w.fail(rw, err)
		return
	}
	writeJSON(rw, j)
}

func (w *Web) sessionStatus(rw http.ResponseWriter, r *http.Request) {
	ss, err := w.service.Store.Session(r.PathValue("id"))
	if err != nil {
		w.fail(rw, err)
		return
	}
	writeJSON(rw, ss)
}

func (w *Web) frame(rw http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.fail(rw, ErrNotFound)
		return
	}
	w.service.filesMu.RLock()
	defer w.service.filesMu.RUnlock()
	c, err := w.service.Store.Capture(id)
	if err != nil {
		w.fail(rw, err)
		return
	}
	if c.Status != "saved" {
		w.fail(rw, ErrNotFound)
		return
	}
	w.serveFile(rw, r, filepath.Join("sessions", c.SessionID, c.File), c.File, "image/jpeg", false)
}

func (w *Web) downloadVideo(rw http.ResponseWriter, r *http.Request) {
	w.service.filesMu.RLock()
	defer w.service.filesMu.RUnlock()
	j, err := w.service.Store.Job(r.PathValue("id"))
	if err != nil {
		w.fail(rw, err)
		return
	}
	if j.State != "ready" {
		w.fail(rw, ErrConflict)
		return
	}
	w.serveFile(rw, r, jobPath(j), videoFilename(j.Manifest.Session.Name), "video/mp4", true)
}

// Download names are presentation only; stored exports retain their UUID paths.
func videoFilename(name string) string {
	return safeFilename(name) + ".mp4"
}

func safeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	var b strings.Builder
	for _, r := range name {
		if b.Len()+len(string(r)) > 180 {
			break
		}
		b.WriteRune(r)
	}
	name = strings.Trim(b.String(), " .")
	if name == "" {
		name = "Untitled session"
	}
	base, _, _ := strings.Cut(strings.ToUpper(name), ".")
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
		name = "_" + name
	}
	return name
}

func (w *Web) exportName(s Session) string {
	return "snapshot-buddy-" + safeFilename(printerDisplayName(w.service.Config, s.PrinterID)) + "-" + s.OpenedAt.UTC().Format("20060102-150405") + "-" + s.ID[:8]
}

func (w *Web) serveFile(rw http.ResponseWriter, r *http.Request, path, name, contentType string, download bool) {
	f, err := w.service.Root.Open(path)
	if err != nil {
		w.fail(rw, ErrNotFound)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		w.fail(rw, ErrNotFound)
		return
	}
	rw.Header().Set("Content-Type", contentType)
	if download {
		rw.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	http.ServeContent(rw, r, name, stat.ModTime(), f)
}

func (w *Web) archive(rw http.ResponseWriter, r *http.Request) {
	s := w.service
	s.filesMu.RLock()
	defer s.filesMu.RUnlock()
	m, err := s.Store.Snapshot(r.PathValue("id"))
	if err != nil {
		w.fail(rw, err)
		return
	}
	// Assign contiguous archive names without changing immutable stored files.
	files := m.Frames()
	n := 0
	for i := range m.Captures {
		if m.Captures[i].Status == "saved" {
			n++
			m.Captures[i].File = fmt.Sprintf("frame_%05d.jpg", n)
		}
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		w.fail(rw, err)
		return
	}
	for _, c := range files {
		info, err := s.Root.Stat(filepath.Join("sessions", m.Session.ID, c.File))
		if err != nil || !info.Mode().IsRegular() || info.Size() != c.Size {
			w.fail(rw, errors.New("a stored frame is missing or changed"))
			return
		}
	}
	rw.Header().Set("Content-Type", "application/x-tar")
	rw.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": w.exportName(m.Session) + ".tar"}))
	tw := tar.NewWriter(rw)
	write := func(name string, size int64, reader io.Reader) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: size, ModTime: m.ExportedAt}); err != nil {
			return err
		}
		_, err := io.Copy(tw, reader)
		return err
	}
	if err = write("manifest.json", int64(len(manifest)), bytes.NewReader(manifest)); err == nil {
		for i, c := range files {
			f, e := s.Root.Open(filepath.Join("sessions", m.Session.ID, c.File))
			if e != nil {
				err = e
				break
			}
			err = write(fmt.Sprintf("frame_%05d.jpg", i+1), c.Size, f)
			f.Close()
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		slog.Error("archive interrupted", "session", m.Session.ID, "error", err)
		panic(http.ErrAbortHandler)
	}
	if err := tw.Close(); err != nil {
		slog.Debug("archive client disconnected", "error", err)
	}
}
