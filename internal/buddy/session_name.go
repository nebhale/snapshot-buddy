package buddy

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidDisplayName = errors.New("Display names must be a single line of at most 200 characters.")
var ErrDisplayNameConflict = errors.New("The display name changed while you were editing. Review the saved name and try again.")

// Title keeps the printer-supplied name intact for provenance and protocol data.
func (s Session) Title() string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return s.Name
}

func cleanDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
		return "", ErrInvalidDisplayName
	}
	return name, nil
}

// SetDisplayName compares only this field so printer and worker progress cannot
// invalidate a draft. An empty override restores the original name.
func (s *Store) SetDisplayName(id, name, expected string) error {
	name, err := cleanDisplayName(name)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ss, err := scanSession(tx.QueryRow(sessionSelect+`WHERE s.id=?`, id))
	if err != nil {
		return err
	}
	if ss.DisplayName != expected {
		return ErrDisplayNameConflict
	}
	if ss.DisplayName == name {
		return nil
	}
	// Naming does not change capture content or invalidate cached video jobs.
	if _, err = tx.Exec(`UPDATE sessions SET display_name=? WHERE id=?`, name, id); err != nil {
		return err
	}
	return s.changed(tx.Commit())
}

func (w *Web) renameSession(rw http.ResponseWriter, r *http.Request) {
	names, ok := r.PostForm["display_name"]
	expected, hasExpected := r.PostForm["expected_display_name"]
	if !ok || len(names) != 1 || !hasExpected || len(expected) != 1 {
		w.render(rw, http.StatusBadRequest, Page{View: "error", Title: "Unable to save display name", Error: "Reload the session and enter a display name."})
		return
	}
	if err := w.service.Store.SetDisplayName(r.PathValue("id"), names[0], expected[0]); err != nil {
		w.fail(rw, err)
		return
	}
	http.Redirect(rw, r, "/sessions/"+r.PathValue("id"), http.StatusSeeOther)
}
