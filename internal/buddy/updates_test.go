package buddy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveCaptureDeltasAndRegions(t *testing.T) {
	s, _ := testService(t)
	handle(t, s, "START core-one Live print", time.Now())
	id := active(t, s, "core-one").ID
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	read := func(query string) livePage {
		t.Helper()
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest("GET", "/api/live?view=session&id="+id+query, nil))
		if rw.Code != 200 {
			t.Fatal(rw.Code, rw.Body.String())
		}
		var page livePage
		if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := read("")
	if first.CSRF == "" || first.Instance != first.CSRF {
		t.Fatal("missing page credentials")
	}
	known := map[string]string{}
	for _, r := range first.Regions {
		known[r.ID] = r.Version
		if r.ID == "provenance" {
			t.Fatal("closed audit was transferred")
		}
	}
	b, _ := json.Marshal(known)
	repeat := read("&known=" + url.QueryEscape(string(b)))
	if len(repeat.Regions) != 1 || repeat.Regions[0].ID != "frames" {
		t.Fatal("unchanged regions retransmitted", repeat.Regions)
	}
	e, err := ParseMarker("M118 SB1 LAYER core-one 1")
	if err != nil {
		t.Fatal(err)
	}
	e.ReceivedAt = time.Now()
	e.SourceIP = "127.0.0.1"
	ch, stop := s.Store.changes.subscribe()
	defer stop()
	capture, _, err := s.Store.Apply(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("committed capture did not notify")
	}
	pending := read("")
	var found bool
	for _, r := range pending.Regions {
		if r.ID == "frames" {
			found = strings.Contains(r.HTML, `data-capture-state="pending"`)
		}
	}
	if !found {
		t.Fatal("pending capture missing")
	}
	capture.Status = "saved"
	capture.File = "frame.jpg"
	if err := s.Store.FinishCapture(*capture); err != nil {
		t.Fatal(err)
	}
	delta := read("&after=" + strconv.FormatInt(capture.ID, 10) + "&pending=" + strconv.FormatInt(capture.ID, 10))
	found = false
	for _, r := range delta.Regions {
		if r.ID == "frames" {
			found = strings.Contains(r.HTML, `data-capture-state="saved"`)
		}
	}
	if !found {
		t.Fatal("pending-to-saved transition missing")
	}
	settled := read("&after=" + strconv.FormatInt(capture.ID, 10))
	for _, r := range settled.Regions {
		if r.ID == "frames" && strings.Contains(r.HTML, `data-capture=`) {
			t.Fatal("immutable capture retransmitted")
		}
	}
	for _, r := range read("&audit=true").Regions {
		if r.ID == "provenance" {
			return
		}
	}
	t.Fatal("expanded audit missing")
}

func TestLiveActionsAndAuthentication(t *testing.T) {
	s, _ := testService(t)
	handle(t, s, "START core-one Live print", time.Now())
	id := active(t, s, "core-one").ID
	s.Config.AuthUser = "buddy"
	s.Config.AuthPassword = "secret"
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/events", "/api/live?view=dashboard"} {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest("GET", path, nil))
		if rw.Code != 401 {
			t.Fatal(path, rw.Code)
		}
	}
	get := httptest.NewRequest("GET", "/api/live?view=session&id="+id, nil)
	get.SetBasicAuth("buddy", "secret")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, get)
	var page livePage
	json.Unmarshal(rw.Body.Bytes(), &page)
	post := func(csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/sessions/"+id+"/close", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()))
		r.SetBasicAuth("buddy", "secret")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "application/json")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	if response := post("invalid"); response.Code != 403 {
		t.Fatal(response.Code)
	}
	response := post(page.CSRF)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"location"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := post(page.CSRF); response.Code != 409 || !strings.Contains(response.Body.String(), `"error"`) {
		t.Fatal(response.Code, response.Body.String())
	}
}
