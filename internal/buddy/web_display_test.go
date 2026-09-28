package buddy

import (
	"html"
	"mime"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func visiblePageText(body string) string {
	return html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, ""))
}

func TestPrinterDisplayFallbacks(t *testing.T) {
	c := Config{Printers: []Printer{{ID: "c1", Name: "Prusa CORE One"}, {ID: "c2", Name: "c2"}, {ID: "c3"}}}
	for id, want := range map[string]string{"c1": "Prusa CORE One", "c2": "Unnamed printer", "c3": "Unnamed printer", "removed": "Unknown printer"} {
		if got := printerDisplayName(c, id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}

func TestMarkerDescriptions(t *testing.T) {
	c := Config{Printers: []Printer{{ID: "c1", Name: "Prusa CORE One"}}}
	for raw, want := range map[string]string{
		"M118 SB1 START c1 c1 fixture":      "START · Prusa CORE One · c1 fixture",
		"invalid":                           "Unrecognized marker",
		"M118 SB1 LAYER c1 7":               "LAYER · Prusa CORE One · Layer 7",
		"M118 SNAPSHOT_BUDDY_V1 FRAME c1 8": "LAYER · Prusa CORE One · Layer 8",
		"M118 SB1 STOP c1 9":                "STOP · Prusa CORE One · Layer 9",
	} {
		if got := markerDescription(c, raw); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

func TestVisiblePrinterNames(t *testing.T) {
	s, _ := testService(t)
	s.Config.Printers[0].Name = "Prusa <CORE> One"
	handle(t, s, "START core-one Widget", time.Now())
	id := active(t, s, "core-one").ID
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/sessions/" + id} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		text := visiblePageText(w.Body.String())
		if w.Code != 200 || !strings.Contains(text, "Prusa <CORE> One") || strings.Contains(text, "core-one") {
			t.Fatalf("%s: status %d, text %s", path, w.Code, text)
		}
		if strings.Contains(w.Body.String(), "Prusa <CORE> One") {
			t.Fatal("printer name was not escaped")
		}
	}
	setup := httptest.NewRecorder()
	h.ServeHTTP(setup, httptest.NewRequest("GET", "/setup", nil))
	if !strings.Contains(visiblePageText(setup.Body.String()), "M118 SB1 START core-one ") {
		t.Fatal("G-code lost the printer ID")
	}
	archive := httptest.NewRecorder()
	h.ServeHTTP(archive, httptest.NewRequest("GET", "/sessions/"+id+"/archive.tar", nil))
	_, params, err := mime.ParseMediaType(archive.Header().Get("Content-Disposition"))
	if err != nil || !strings.HasPrefix(params["filename"], "snapshot-buddy-Prusa _CORE_ One-") {
		t.Fatalf("download name: %v %v", params, err)
	}
	m, err := s.Store.Snapshot(id)
	if err != nil || m.Session.PrinterID != "core-one" || m.Session.InitialMarker != "M118 SB1 START core-one Widget" {
		t.Fatal("stored protocol data changed")
	}
}
