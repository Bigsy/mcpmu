package web

import (
	"log"
	"net/http"
	"slices"

	"github.com/Bigsy/mcpmu/internal/events"
)

// handleFragmentServerTable returns the server table HTML fragment.
// Used by htmx polling to refresh the server list without a full page reload.
func (s *Server) handleFragmentServerTable(w http.ResponseWriter, r *http.Request) {
	entries := s.cfg.ServerEntries()
	statuses := s.status.All()

	var rows []serverRow
	for _, entry := range entries {
		row := s.buildServerRow(entry.Name, entry.Config, statuses)
		for nsName, nsCfg := range s.cfg.Namespaces {
			if slices.Contains(nsCfg.ServerIDs, entry.Name) {
				row.Namespaces = append(row.Namespaces, nsName)
			}
		}
		rows = append(rows, row)
	}

	data := struct{ Servers []serverRow }{Servers: rows}
	s.renderFragment(w, "server_table", data)
}

// handleFragmentServerStatus returns a server status pill HTML fragment.
// Used by htmx to poll for status changes on the detail page.
func (s *Server) handleFragmentServerStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if _, ok := s.cfg.GetServer(name); !ok {
		http.NotFound(w, r)
		return
	}

	st, ok := s.status.Get(name)
	if !ok {
		st = events.ServerStatus{State: events.StateIdle}
	}

	s.renderFragment(w, "server_status", st)
}

// renderFragment executes a named template definition (partial) and writes
// the HTML fragment to the response. Unlike render(), this does not wrap
// in the layout — it returns only the fragment for htmx swaps.
func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	// Fragments use the base template set (which includes all partials).
	// Any page template clone has the partials, so pick the first one.
	// We store a dedicated "fragments" template for this purpose.
	tmpl, ok := s.templates["_fragments"]
	if !ok {
		log.Printf("fragment template set not found")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("fragment %s: %v", name, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
