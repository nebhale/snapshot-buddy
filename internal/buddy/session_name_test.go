package buddy

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDisplayNameFormsAndLiveViews(t *testing.T) {
	s, _ := testService(t)
	handle(t, s, "START core-one Print", time.Now())
	id := active(t, s, "core-one").ID
	s.Config.AuthUser, s.Config.AuthPassword = "buddy", "secret"
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, data url.Values, auth, jsonResponse bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(data.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if jsonResponse {
			r.Header.Set("Accept", "application/json")
		}
		if auth {
			r.SetBasicAuth("buddy", "secret")
		}
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, r)
		return rw
	}
	live := request("GET", "/api/live?view=session&id="+id, nil, true, true)
	var page livePage
	if err := json.Unmarshal(live.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	name := "Vase <blue> & café 🌿"
	data := url.Values{"csrf": {page.CSRF}, "display_name": {"  " + name + "  "}, "expected_display_name": {""}}
	path := "/sessions/" + id + "/name"
	if rw := request("POST", path, data, false, true); rw.Code != 401 {
		t.Fatal(rw.Code)
	}
	data.Set("csrf", "wrong")
	if rw := request("POST", path, data, true, true); rw.Code != 403 {
		t.Fatal(rw.Code)
	}
	data.Set("csrf", page.CSRF)
	if rw := request("POST", path, data, true, true); rw.Code != 200 {
		t.Fatal(rw.Code, rw.Body.String())
	}
	for _, path := range []string{"/", "/sessions/" + id} {
		rw := request("GET", path, nil, true, false)
		if rw.Code != 200 || !strings.Contains(rw.Body.String(), "Vase &lt;blue&gt; &amp; café 🌿") || strings.Contains(rw.Body.String(), name) {
			t.Fatal(path, rw.Code, rw.Body.String())
		}
	}
	live = request("GET", "/api/live?view=session&id="+id, nil, true, true)
	if err := json.Unmarshal(live.Body.Bytes(), &page); err != nil || page.Title != name {
		t.Fatal(err, page.Title)
	}
	found := false
	for _, region := range page.Regions {
		if region.ID == "session-heading" && strings.Contains(region.HTML, "Vase &lt;blue&gt;") {
			found = true
		}
	}
	if !found {
		t.Fatal("renamed heading missing from live update")
	}
	if rw := request("POST", path, data, true, true); rw.Code != 409 || !strings.Contains(rw.Body.String(), "display name changed") {
		t.Fatal(rw.Code, rw.Body.String())
	}
	data.Set("expected_display_name", name)
	for _, invalid := range []string{strings.Repeat("a", 201), "two\nlines", "bad\x00name", "bad\u2028name", string([]byte{0xff})} {
		data.Set("display_name", invalid)
		if rw := request("POST", path, data, true, true); rw.Code != 400 {
			t.Fatalf("invalid name %q: %d %s", invalid, rw.Code, rw.Body.String())
		}
	}
	data.Set("display_name", " ")
	if rw := request("POST", path, data, true, false); rw.Code != 303 || rw.Header().Get("Location") != "/sessions/"+id {
		t.Fatal(rw.Code, rw.Body.String())
	}
	live = request("GET", "/api/live?view=session&id="+id, nil, true, true)
	if err := json.Unmarshal(live.Body.Bytes(), &page); err != nil || page.Title != "Print" {
		t.Fatal(err, page.Title)
	}
	data.Del("expected_display_name")
	if rw := request("POST", path, data, true, true); rw.Code != 400 {
		t.Fatal("missing expectation accepted", rw.Code)
	}
	data.Set("expected_display_name", "")
	if rw := request("POST", "/sessions/missing/name", data, true, true); rw.Code != 404 {
		t.Fatal(rw.Code)
	}
}

func TestDisplayNamePreservesCapturesAndSessionIdentity(t *testing.T) {
	s, _ := testService(t)
	at := time.Now()
	handle(t, s, "START core-one Print", at)
	before := active(t, s, "core-one")
	handle(t, s, "LAYER core-one 1", at.Add(time.Second))
	ch, stop := s.Store.changes.subscribe()
	defer stop()
	current := active(t, s, "core-one")
	if err := s.Store.SetDisplayName(before.ID, "Desk organizer", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("rename did not notify live views")
	}
	renamed := active(t, s, "core-one")
	if renamed.Name != before.Name || renamed.InitialMarker != before.InitialMarker || renamed.Revision != current.Revision || renamed.FrameCount != current.FrameCount {
		t.Fatal("rename changed capture or protocol state", renamed)
	}
	if err := s.Store.SetDisplayName(before.ID, "Lost edit", ""); !errors.Is(err, ErrDisplayNameConflict) {
		t.Fatal(err)
	}
	handle(t, s, "START core-one Print", at.Add(2200*time.Millisecond))
	if active(t, s, "core-one").ID != before.ID {
		t.Fatal("START retry split renamed session")
	}
	handle(t, s, "STOP core-one 2", at.Add(3*time.Second))
	closed, err := s.Store.Session(before.ID)
	if err != nil || closed.Title() != "Desk organizer" || closed.State != "closed" || closed.FrameCount != 2 {
		t.Fatal(closed, err)
	}
	if err := s.Store.SetDisplayName(before.ID, "Finished organizer", "Desk organizer"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s.Store, err = OpenStore(s.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	closed, err = s.Store.Session(before.ID)
	if err != nil || closed.Title() != "Finished organizer" || closed.Name != "Print" {
		t.Fatal(closed, err)
	}
	handle(t, s, "START core-one Print", at.Add(10*time.Second))
	next := active(t, s, "core-one")
	if next.ID == before.ID || next.DisplayName != "" || next.Title() != "Print" {
		t.Fatal("new print inherited an override", next)
	}
}

func TestDisplayNameMigrationAndRecoveredSession(t *testing.T) {
	s, _ := testService(t)
	handle(t, s, "LAYER core-one 1", time.Now())
	before := active(t, s, "core-one")
	if _, err := s.Store.db.Exec(`ALTER TABLE sessions DROP COLUMN display_name; PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s.Store, err = OpenStore(s.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	current := active(t, s, "core-one")
	if current.Title() != before.Name || current.DisplayName != "" {
		t.Fatal(current)
	}
	if err := s.Store.SetDisplayName(current.ID, "Recovered vase", ""); err != nil {
		t.Fatal(err)
	}
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest("GET", "/sessions/"+current.ID, nil))
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), "<h1>Recovered vase</h1>") {
		t.Fatal(rw.Code, rw.Body.String())
	}
	if err := s.Store.SetDisplayName(current.ID, "", "Recovered vase"); err != nil {
		t.Fatal(err)
	}
	if active(t, s, "core-one").Title() != before.Name {
		t.Fatal("original recovered name lost")
	}
}
