package buddy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestLifecycleRetriesAcrossStoreRestarts(t *testing.T) {
	for _, prefix := range []string{markerPrefix, legacyMarkerPrefix} {
		// Every nonempty delivery subset, including loss of both early copies.
		for delivered := 1; delivered < 8; delivered++ {
			t.Run(fmt.Sprintf("%s/copies_%03b", prefix, delivered), func(t *testing.T) {
				s, cam := testService(t)
				at := time.Now().UTC()
				sessionID := ""
				for _, kind := range []string{"START", "STOP"} {
					payload := "Retry test"
					if kind == "STOP" {
						payload = "42"
					}
					for i := 0; i < 3; i++ {
						if delivered&(1<<i) == 0 {
							continue
						}
						e, err := ParseMarker(prefix + kind + " core-one " + payload)
						if err != nil {
							t.Fatal(err)
						}
						e.SourceIP = "127.0.0.1"
						e.ReceivedAt = at.Add(time.Duration(i) * 1100 * time.Millisecond)
						if err := s.Handle(context.Background(), e); err != nil {
							t.Fatal(err)
						}
						if kind == "START" {
							current := active(t, s, "core-one")
							if sessionID != "" && current.ID != sessionID {
								t.Fatal("retry superseded the original session")
							}
							sessionID = current.ID
						}
						if s.printers["core-one"].lastError != "" {
							t.Fatalf("retry produced a diagnostic: %s", s.printers["core-one"].lastError)
						}
						// Reopen durable state between every delivered copy.
						if err := s.Store.Close(); err != nil {
							t.Fatal(err)
						}
						s.Store, err = OpenStore(s.Config.DataDir)
						if err != nil {
							t.Fatal(err)
						}
					}
					at = at.Add(time.Minute)
				}
				if _, err := s.Store.Active("core-one"); !errors.Is(err, ErrNotFound) {
					t.Fatalf("session remained active after STOP: %v", err)
				}
				sessions, err := s.Store.Sessions("core-one", 100, 0)
				if err != nil || len(sessions) != 1 {
					t.Fatalf("sessions: %+v, error: %v", sessions, err)
				}
				m, err := s.Store.Snapshot(sessionID)
				if err != nil {
					t.Fatal(err)
				}
				if m.Session.CloseReason != "completed" || m.Session.Recovered || m.Session.FrameCount != 1 || len(m.Captures) != 1 || cam.calls != 1 {
					t.Fatalf("retry duplicated lifecycle or capture: %+v, camera calls: %d", m, cam.calls)
				}
			})
		}
	}
}

func TestDuplicateWindowsRemainBounded(t *testing.T) {
	for _, tc := range []struct {
		payload string
		window  time.Duration
	}{
		{"START core-one Same print", 5 * time.Second},
		{"STOP core-one 42", 5 * time.Second},
		{"LAYER core-one 42", 2 * time.Second},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			s, _ := testService(t)
			at := time.Now().UTC()
			handle(t, s, "START core-one Earlier print", at.Add(-time.Minute))
			apply := func(offset time.Duration) string {
				t.Helper()
				_, result, err := s.Store.Apply(context.Background(), marker(t, tc.payload, at.Add(offset)))
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			first := apply(0)
			if first == "duplicate" || first == "orphan_stop" {
				t.Fatalf("first copy not accepted: %s", first)
			}
			for _, offset := range []time.Duration{100 * time.Millisecond, 1100 * time.Millisecond, tc.window - time.Millisecond} {
				if got := apply(offset); got != "duplicate" {
					t.Fatalf("copy at %s not deduplicated: %s", offset, got)
				}
			}
			// Repeated copies must not slide the window forward indefinitely.
			if got := apply(tc.window); got == "duplicate" {
				t.Fatal("duplicate window was extended by a retry")
			}
		})
	}
}

func TestLifecycleRetriesDoNotChangeNewSessionHandling(t *testing.T) {
	s, _ := testService(t)
	at := time.Now().UTC()
	handle(t, s, "START core-one First print", at)
	first := active(t, s, "core-one")
	handle(t, s, "START mini Other printer", at.Add(time.Second))
	handle(t, s, "START core-one Different print", at.Add(time.Second))
	next := active(t, s, "core-one")
	if next.ID == first.ID || next.Name != "Different print" {
		t.Fatal("different START was suppressed inside the retry window")
	}
	closed, err := s.Store.Session(first.ID)
	if err != nil || closed.CloseReason != "superseded" {
		t.Fatalf("old session was not superseded: %+v, %v", closed, err)
	}
	handle(t, s, "START core-one Different print", at.Add(6*time.Second))
	if active(t, s, "core-one").ID == next.ID {
		t.Fatal("same-name START was suppressed after the retry window")
	}
	if active(t, s, "mini").Name != "Other printer" {
		t.Fatal("another printer's session was changed")
	}
}

func TestLayerSnippetTimingUnchanged(t *testing.T) {
	c := testConfig(t)
	want := `; BEGIN Snapshot Buddy: layer snapshot (core-one)
M400
M331 gcode
M118 SB1 LAYER core-one {layer_num}
G4 P100
M118 SB1 LAYER core-one {layer_num}
M332 gcode
; END Snapshot Buddy: layer snapshot (core-one)`
	if got := GCode(c, c.Printers[0]).Layer; got != want {
		t.Fatalf("layer block changed:\n%s", got)
	}
}
