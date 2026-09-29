package buddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

type bulkHarness struct {
	t     *testing.T
	s     *Service
	h     http.Handler
	token string
	seq   int
}

func newBulkHarness(t *testing.T) *bulkHarness {
	t.Helper()
	s, _ := testService(t)
	h, err := NewWeb(s)
	if err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/live?view=dashboard", nil))
	var page livePage
	if err := json.Unmarshal(out.Body.Bytes(), &page); err != nil {
		t.Fatal(err, out.Body.String())
	}
	return &bulkHarness{t: t, s: s, h: h, token: page.CSRF}
}
func (b *bulkHarness) session(closed bool) string {
	b.t.Helper()
	b.seq++
	at := time.Now().Add(time.Duration(b.seq) * 2 * time.Hour)
	handle(b.t, b.s, fmt.Sprintf("START core-one Bulk %d", b.seq), at)
	id := active(b.t, b.s, "core-one").ID
	handle(b.t, b.s, "LAYER core-one 1", at.Add(time.Minute))
	if closed {
		if err := b.s.CloseSession(id); err != nil {
			b.t.Fatal(err)
		}
	}
	return id
}
func (b *bulkHarness) form(ids ...string) url.Values {
	values := url.Values{"csrf": {b.token}, "session": ids, "page": {"0"}, "confirmed": {"true"}}

	return values
}
func (b *bulkHarness) post(action string, values url.Values, enhanced bool) *httptest.ResponseRecorder {
	b.t.Helper()
	r := httptest.NewRequest("POST", "/sessions/bulk/"+action, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if enhanced {
		r.Header.Set("Accept", "application/json")
	}
	out := httptest.NewRecorder()
	b.h.ServeHTTP(out, r)
	return out
}
func (b *bulkHarness) result(action string, values url.Values) bulkReply {
	b.t.Helper()
	out := b.post(action, values, true)
	if out.Code != 200 {
		b.t.Fatal(out.Code, out.Body.String())
	}
	var result bulkReply
	if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
		b.t.Fatal(err, out.Body.String())
	}
	return result
}
func bulkStatuses(reply bulkReply) map[string]string {
	statuses := map[string]string{}
	for _, result := range reply.Results {
		statuses[result.ID] = result.Status
	}
	return statuses
}

func TestBulkRejectsInvalidSelectionBeforeAnyMutation(t *testing.T) {
	for _, kind := range []string{"empty", "duplicate", "malformed", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			b := newBulkHarness(t)
			id := b.session(true)
			values := b.form(id)
			switch kind {
			case "empty":
				values.Del("session")
			case "duplicate":
				values.Add("session", id)
			case "malformed":
				values.Add("session", "../outside")
			case "oversize":
				for i := 0; i < 24; i++ {
					values.Add("session", fmt.Sprintf("missing-%d", i))
				}

			}
			out := b.post("delete", values, true)
			if out.Code != 400 {
				t.Fatal(out.Code, out.Body.String())
			}
			ss, err := b.s.Store.Session(id)
			if err != nil {
				t.Fatal("valid item was changed before request validation", ss, err)
			}
		})
	}
}

func TestBulkAuthenticationAndCSRF(t *testing.T) {
	b := newBulkHarness(t)
	id := b.session(true)
	form := b.form(id)
	form.Set("csrf", "invalid")
	if out := b.post("delete", form, true); out.Code != 403 {
		t.Fatal(out.Code)
	}
	b.s.Config.AuthUser, b.s.Config.AuthPassword = "buddy", "secret"
	form.Set("csrf", b.token)
	if out := b.post("delete", form, true); out.Code != 401 {
		t.Fatal(out.Code)
	}
	r := httptest.NewRequest("POST", "/sessions/bulk/delete", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	r.SetBasicAuth("buddy", "secret")
	out := httptest.NewRecorder()
	b.h.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
}

func TestBulkEmptyLastPageReturnsToPreviousPage(t *testing.T) {
	b := newBulkHarness(t)
	first := b.session(true)
	for i := 0; i < 24; i++ {
		b.session(true)
	}
	form := b.form(first)
	form.Set("page", "1")
	form.Set("printer", "core-one")
	reply := b.result("delete", form)
	location, err := url.Parse(reply.Location)
	if err != nil || location.Query().Get("page") != "0" || location.Query().Get("printer") != "core-one" {
		t.Fatal(reply.Location, err)
	}
}

func TestBulkDeleteMixedResultsRemoveOnlyEligibleMedia(t *testing.T) {
	b := newBulkHarness(t)
	first, busy, activeID := b.session(true), b.session(true), b.session(false)
	job, err := b.s.RequestVideo(busy, 10000)
	if err != nil {
		t.Fatal(err)
	}
	reply := b.result("delete", b.form(first, busy, activeID, "missing"))
	statuses := bulkStatuses(reply)
	if statuses[first] != "success" || statuses[busy] != "conflict" || statuses[activeID] != "conflict" || statuses["missing"] != "missing" {
		t.Fatal(reply)
	}
	if _, err := b.s.Store.Session(first); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted metadata remains", err)
	}
	if _, err := b.s.Root.Stat("sessions/" + first); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted media remains", err)
	}
	if _, err := b.s.Root.Stat("sessions/" + busy); err != nil {
		t.Fatal("busy media was removed", err)
	}
	if err := b.s.Store.FinishJob(job.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if status := bulkStatuses(b.result("delete", b.form(busy)))[busy]; status != "conflict" {
		t.Fatal("running job was not protected", status)
	}
	if err := b.s.Store.FinishJob(job.ID, "ready", ""); err != nil {
		t.Fatal(err)
	}
	// A completed video shares the session directory with the original images.
	if err := b.s.Root.WriteFile("sessions/"+busy+"/generated.mp4", []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if status := bulkStatuses(b.result("delete", b.form(busy)))[busy]; status != "success" {
		t.Fatal(status)
	}
	if _, err := b.s.Root.Stat("sessions/" + busy); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("generated media remains", err)
	}
}

func TestBulkDeleteProtectsPendingCapturesAndDownloads(t *testing.T) {
	b := newBulkHarness(t)
	id := b.session(false)
	e := marker(t, "LAYER core-one 2", time.Now().Add(4*time.Hour))
	if _, _, err := b.s.Store.Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := b.s.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	if status := bulkStatuses(b.result("delete", b.form(id)))[id]; status != "conflict" {
		t.Fatal("pending capture not protected", status)
	}
	other := b.session(true)
	form := b.form(other)
	b.s.filesMu.RLock()
	start := time.Now()
	reply := b.result("delete", form)
	b.s.filesMu.RUnlock()
	if time.Since(start) > time.Second || bulkStatuses(reply)[other] != "conflict" {
		t.Fatal("deletion waited for a download", reply)
	}
	if bulkStatuses(b.result("delete", form))[other] != "success" {
		t.Fatal("session remained blocked after download")
	}
}

func TestBulkNativeDeletionRequiresConfirmationAndEscapesNames(t *testing.T) {
	b := newBulkHarness(t)
	id, busy := b.session(true), b.session(true)
	if err := b.s.Store.SetDisplayName(id, "<script>alert(1)</script>", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.RequestVideo(busy, 10000); err != nil {
		t.Fatal(err)
	}
	form := b.form(id, busy)
	form.Del("confirmed")
	out := b.post("delete", form, false)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "Delete 2 selected sessions?") || strings.Contains(out.Body.String(), "<script>alert") {
		t.Fatal(out.Code, out.Body.String())
	}
	if _, err := b.s.Store.Session(id); err != nil {
		t.Fatal("deleted before confirmation", err)
	}
	if out := b.post("delete", form, true); out.Code != 400 {
		t.Fatal("enhanced action bypassed confirmation", out.Code)
	}
	form.Set("confirmed", "true")
	out = b.post("delete", form, false)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "1 session deleted.") {
		t.Fatal(out.Code, out.Body.String())
	}
	if !regexp.MustCompile(`value="` + busy + `"[^>]+checked`).MatchString(out.Body.String()) {
		t.Fatal("blocked selection was lost")
	}
}
