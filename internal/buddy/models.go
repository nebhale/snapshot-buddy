package buddy

import "time"

type Session struct {
	ID            string     `json:"id"`
	PrinterID     string     `json:"printer_id"`
	Name          string     `json:"name"`
	State         string     `json:"state"`
	CloseReason   string     `json:"close_reason,omitempty"`
	OpenedAt      time.Time  `json:"opened_at"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	Recovered     bool       `json:"recovered"`
	OpenedBy      string     `json:"opened_by"`
	FirstLayer    *int       `json:"first_layer,omitempty"`
	SourceIP      string     `json:"source_ip"`
	InitialMarker string     `json:"initial_marker"`
	Revision      int        `json:"revision"`
	FrameCount    int        `json:"frame_count"`
	FailureCount  int        `json:"failure_count"`
	PendingCount  int        `json:"pending_count"`
}

type Capture struct {
	ID         int64      `json:"id"`
	SessionID  string     `json:"session_id"`
	Kind       string     `json:"kind"`
	Layer      int        `json:"layer"`
	ReceivedAt time.Time  `json:"received_at"`
	CapturedAt *time.Time `json:"captured_at,omitempty"`
	SourceIP   string     `json:"source_ip"`
	Raw        string     `json:"raw"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	File       string     `json:"file,omitempty"`
	Width      int        `json:"width,omitempty"`
	Height     int        `json:"height,omitempty"`
	Size       int64      `json:"size,omitempty"`
}

type Manifest struct {
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	Session    Session   `json:"session"`
	Captures   []Capture `json:"captures"`
}

func (m Manifest) Frames() []Capture {
	var frames []Capture
	for _, c := range m.Captures {
		if c.Status == "saved" {
			frames = append(frames, c)
		}
	}
	return frames
}

type Job struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Revision   int       `json:"revision"`
	DurationMS int64     `json:"duration_ms"`
	FrameCount int       `json:"frame_count"`
	State      string    `json:"state"`
	Progress   float64   `json:"progress"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Manifest   Manifest  `json:"-"`
}
