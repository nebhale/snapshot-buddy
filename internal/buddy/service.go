package buddy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type printerRuntime struct {
	operationMu sync.Mutex
	mu          sync.Mutex
	printer     Printer
	queue       chan Event
	suppressed  bool
	lastEvent   time.Time
	lastError   string
	dropped     uint64
}

type PrinterStatus struct {
	Printer    Printer   `json:"printer"`
	Suppressed bool      `json:"suppressed"`
	LastEvent  time.Time `json:"last_event"`
	LastError  string    `json:"last_error,omitempty"`
	Dropped    uint64    `json:"dropped"`
	Active     *Session  `json:"active,omitempty"`
	Latest     *Capture  `json:"latest,omitempty"`
}

type Service struct {
	Config   Config
	Store    *Store
	Root     *os.Root
	camera   Capturer
	printers map[string]*printerRuntime
	// Protect exports and capture files against concurrent deletion.
	filesMu sync.RWMutex
	wg      sync.WaitGroup
}

func NewService(c Config, store *Store, camera Capturer) (*Service, error) {
	root, err := os.OpenRoot(c.DataDir)
	if err != nil {
		return nil, err
	}
	if err := root.MkdirAll("sessions", 0750); err != nil {
		root.Close()
		return nil, err
	}
	s := &Service{Config: c, Store: store, Root: root, camera: camera, printers: map[string]*printerRuntime{}}
	for _, p := range c.Printers {
		s.printers[p.ID] = &printerRuntime{printer: p, queue: make(chan Event, 64)}
	}
	return s, nil
}

func (s *Service) Start(ctx context.Context) {
	for _, p := range s.printers {
		s.wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case e := <-p.queue:
					if ctx.Err() != nil {
						return
					}
					if err := s.Handle(ctx, e); err != nil {
						slog.Error("printer event failed", "printer", e.PrinterID, "error", err)
					}
				}
			}
		})
	}
}

func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) Dispatch(e Event) bool {
	p, ok := s.printers[e.PrinterID]
	if !ok {
		slog.Warn("unknown printer marker", "printer", e.PrinterID)
		return false
	}
	if !allowedSource(p.printer, e.SourceIP) {
		slog.Warn("rejected marker source", "printer", e.PrinterID, "source", e.SourceIP)
		return false
	}
	select {
	case p.queue <- e:
		return true
	default:
		// Do not stall reception for other printers when one camera is slow.
		p.mu.Lock()
		defer s.Store.changes.publish()
		p.dropped++
		p.lastError = "Capture queue is full; a printer marker was dropped"
		p.mu.Unlock()
		slog.Warn("printer queue full", "printer", e.PrinterID)
		return false
	}
}

func allowedSource(p Printer, source string) bool {
	if p.SourceIP == "" {
		return true
	}
	a, e1 := netip.ParseAddr(p.SourceIP)
	b, e2 := netip.ParseAddr(source)
	return e1 == nil && e2 == nil && a.Unmap() == b.Unmap()
}

// Handle is serialized per printer, including capture, so completion and a new
// START cannot move a frame into the wrong session.
func (s *Service) Handle(ctx context.Context, e Event) error {
	defer s.Store.changes.publish()
	p, ok := s.printers[e.PrinterID]
	if !ok || !allowedSource(p.printer, e.SourceIP) {
		return errors.New("unconfigured printer or unexpected source IP")
	}
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	p.mu.Lock()
	p.lastEvent = e.ReceivedAt
	suppressed := p.suppressed
	p.mu.Unlock()
	if suppressed && e.Kind == "LAYER" {
		return nil
	}
	capture, result, err := s.Store.Apply(ctx, e)
	if err != nil {
		p.mu.Lock()
		p.lastError = err.Error()
		p.mu.Unlock()
		return err
	}
	if result == "orphan_stop" {
		p.mu.Lock()
		p.lastError = "STOP received without an active session; no session was created"
		p.mu.Unlock()
		slog.Warn("STOP without active session", "printer", e.PrinterID, "layer", e.Layer, "source", e.SourceIP, "received_at", e.ReceivedAt, "marker", e.Raw)
		return nil
	}
	if result == "duplicate" {
		slog.Debug("ignored marker", "printer", e.PrinterID, "reason", result)
		return nil
	}
	if e.Kind == "START" {
		p.mu.Lock()
		p.suppressed = false
		p.mu.Unlock()
	}
	ss, _ := s.Store.Session(result)
	slog.Info("printer marker", "printer", e.PrinterID, "event", e.Kind, "session", result, "layer", e.Layer, "recovered", ss.Recovered, "close_reason", ss.CloseReason, "source", e.SourceIP)
	if capture == nil {
		return nil
	}
	s.filesMu.RLock()
	defer s.filesMu.RUnlock()
	var img Image
	// A backed-up queue cannot reconstruct an earlier layer. Record the miss
	// instead of taking a misleading snapshot long after its trigger.
	if time.Since(e.ReceivedAt) > s.Config.Capture.Timeout*time.Duration(s.Config.Capture.Attempts) {
		err = errors.New("capture marker expired while waiting for the camera")
	} else {
		img, err = s.camera.Capture(ctx, p.printer.Stream)
	}
	if err == nil {
		capture.File = fmt.Sprintf("frame_%08d.jpg", capture.ID)
		dir := filepath.Join("sessions", capture.SessionID)
		err = s.Root.MkdirAll(dir, 0750)
		if err == nil {
			err = atomicWrite(s.Root, filepath.Join(dir, capture.File), img.Bytes)
		}
	}
	if err != nil {
		capture.Status, capture.Error, capture.File = "failed", err.Error(), ""
		p.mu.Lock()
		p.lastError = capture.Error
		p.mu.Unlock()
		slog.Warn("capture failed", "printer", e.PrinterID, "session", capture.SessionID, "layer", e.Layer, "error", err)
	} else {
		now := time.Now().UTC()
		capture.Status, capture.CapturedAt = "saved", &now
		capture.Width, capture.Height, capture.Size = img.Width, img.Height, int64(len(img.Bytes))
		p.mu.Lock()
		p.lastError = ""
		p.mu.Unlock()
	}
	return s.Store.FinishCapture(*capture)
}

func atomicWrite(root *os.Root, path string, data []byte) error {
	temp := path + "." + newID() + ".tmp"
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(temp, path)
}

func (s *Service) CloseSession(id string) error {
	defer s.Store.changes.publish()
	ss, err := s.Store.Session(id)
	if err != nil {
		return err
	}
	if p := s.printers[ss.PrinterID]; p != nil {
		p.operationMu.Lock()
		defer p.operationMu.Unlock()
		if err := s.Store.CloseSession(id); err != nil {
			return err
		}
		p.mu.Lock()
		p.suppressed = true
		p.mu.Unlock()
	} else {
		return s.Store.CloseSession(id)
	}
	slog.Info("session closed", "session", id, "reason", "manual")
	return nil
}

func (s *Service) DeleteSession(id string) error {
	// A waiting writer would block new captures behind a slow download or
	// encoder. Let the user retry deletion without delaying printer events.
	if !s.filesMu.TryLock() {
		return ErrConflict
	}
	defer s.filesMu.Unlock()
	if err := s.Store.CanDelete(id); err != nil {
		return err
	}
	// IDs are generated internally; resolve them through the database before
	// touching files, and keep every operation confined to os.Root.
	if err := s.Root.RemoveAll(filepath.Join("sessions", id)); err != nil {
		return err
	}
	if err := s.Store.Delete(id); err != nil {
		return err
	}
	slog.Info("session deleted", "session", id)
	return nil
}

func (s *Service) Status() ([]PrinterStatus, error) {
	result := []PrinterStatus{}
	for _, config := range s.Config.Printers {
		p := s.printers[config.ID]
		p.mu.Lock()
		v := PrinterStatus{Printer: config, Suppressed: p.suppressed, LastEvent: p.lastEvent, LastError: p.lastError, Dropped: p.dropped}
		p.mu.Unlock()
		ss, err := s.Store.Active(config.ID)
		if err == nil {
			v.Active = &ss
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		capture, err := scanCapture(s.Store.db.QueryRow(captureSelect+`WHERE status='saved' AND session_id IN (SELECT id FROM sessions WHERE printer_id=?) ORDER BY id DESC LIMIT 1`, config.ID))
		if err == nil {
			v.Latest = &capture
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		result = append(result, v)
	}
	return result, nil
}

func (s *Service) ServeUDP(ctx context.Context, conn *net.UDPConn) error {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	buf := make([]byte, 65536)
	for {
		n, addr, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, e := range ParsePacket(buf[:n], addr.Addr().Unmap().String(), time.Now()) {
			s.Dispatch(e)
		}
	}
}
