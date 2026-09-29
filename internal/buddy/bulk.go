package buddy

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// A batch contains explicit page selections, never a filter to expand later.
type bulkItem struct {
	ID, Name string
}
type bulkResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
type bulkReply struct {
	Results  []bulkResult `json:"results"`
	Summary  string       `json:"summary"`
	Location string       `json:"location"`
}

var bulkIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (w *Web) libraryPage(values url.Values) (Page, error) {
	p := Page{View: "dashboard", Title: "Print library", Filter: values.Get("printer"), BulkAction: "delete"}
	p.Page, _ = strconv.Atoi(values.Get("page"))
	p.Page = max(0, min(1000000, p.Page))
	p.Previous = max(0, p.Page-1)
	var err error
	p.Sessions, err = w.service.Store.Sessions(p.Filter, 25, p.Page*24)
	if err != nil {
		return p, err
	}
	if len(p.Sessions) > 24 {
		p.Next = p.Page + 1
		p.Sessions = p.Sessions[:24]
	}
	p.Printers, err = w.service.Status()
	return p, err
}

func bulkLocation(values url.Values) string {
	query := url.Values{}
	query.Set("printer", values.Get("printer"))
	page, _ := strconv.Atoi(values.Get("page"))
	query.Set("page", strconv.Itoa(max(0, min(1000000, page))))

	return "/?" + query.Encode()
}

func parseBulk(values url.Values) ([]bulkItem, error) {
	ids := values["session"]
	if len(ids) == 0 || len(ids) > 24 {
		return nil, errors.New("Select between 1 and 24 sessions on this page.")
	}
	items := make([]bulkItem, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !bulkIDPattern.MatchString(id) || seen[id] {
			return nil, errors.New("Invalid or duplicate session selection.")
		}
		seen[id] = true
		item := bulkItem{ID: id, Name: id}

		items = append(items, item)
	}
	return items, nil
}

func (w *Web) bulkSessions(rw http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/sessions/bulk/")
	if action != "delete" {
		w.fail(rw, ErrNotFound)
		return
	}
	items, err := parseBulk(r.PostForm)
	if err != nil {
		w.render(rw, 400, Page{Title: "Check your selection", Error: err.Error()})
		return
	}
	values := url.Values{"printer": {r.PostForm.Get("printer")}, "page": {r.PostForm.Get("page")}}

	for i := range items {
		ss, err := w.service.Store.Session(items[i].ID)
		if err == nil {
			items[i].Name = ss.Title()
		}
	}
	if r.PostForm.Get("confirmed") != "true" {
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.render(rw, 400, Page{Error: "Confirm permanent deletion before submitting."})
			return
		}
		page, _ := strconv.Atoi(values.Get("page"))
		w.render(rw, 200, Page{View: "bulk-confirm", Title: "Delete selected sessions", Confirm: items, Filter: values.Get("printer"), Page: max(0, min(1000000, page)), ReturnURL: bulkLocation(values)})
		return
	}
	reply := bulkReply{Results: make([]bulkResult, 0, len(items)), Location: bulkLocation(values)}
	successes := 0
	selected := map[string]bool{}
	for _, item := range items {
		result := bulkResult{ID: item.ID, Name: item.Name, Status: "success"}
		err := r.Context().Err()
		if err == nil {
			err = w.service.DeleteSession(item.ID)
		}
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			result.Status, result.Error = "conflict", "Close the session and let captures, video jobs, and downloads finish before trying again."
		case errors.Is(err, ErrNotFound):
			result.Status, result.Error = "missing", "This session no longer exists."
		default:
			slog.Error("bulk session action", "action", action, "session", item.ID, "error", err)
			result.Status, result.Error = "error", "Unable to complete this action. Review the session and service logs before retrying."
		}
		if err != nil {
			selected[item.ID] = true
		}
		reply.Results = append(reply.Results, result)
	}
	verb := map[string]string{"archive": "archived", "restore": "restored", "delete": "deleted"}[action]
	noun := "sessions"
	if successes == 1 {
		noun = "session"
	}
	reply.Summary = fmt.Sprintf("%d %s %s.", successes, noun, verb)
	if failed := len(items) - successes; failed > 0 {
		reply.Summary += fmt.Sprintf(" %d could not be %s.", failed, verb)
	}
	p, err := w.libraryPage(values)
	if err == nil && successes > 0 && len(p.Sessions) == 0 && p.Page > 0 {
		values.Set("page", strconv.Itoa(p.Page-1))
		reply.Location = bulkLocation(values)
		p, err = w.libraryPage(values)
	}
	// Preserve the mutation results even if refreshing the library fails.
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(rw, reply)
		return
	}
	if err != nil {
		p = Page{View: "bulk-result", Title: "Session results", ReturnURL: reply.Location}
	}
	p.Bulk, p.Selected = &reply, selected
	rw.Header().Set("Content-Location", reply.Location)
	w.render(rw, 200, p)
}
