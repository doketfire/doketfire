package api

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/doketfire/doketfire/internal/storage"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	collections := s.store.CollectionCount()
	if err := s.store.View(func(tx storage.ReadTx) error { return nil }); err != nil {
		writeError(w, http.StatusServiceUnavailable, "unhealthy", "storage unavailable", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"version":     s.version,
		"uptime":      int64(time.Since(s.started).Seconds()),
		"collections": collections,
		"dbSize":      s.store.Size(),
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": s.version})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/_/", http.StatusFound)
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		writeError(w, http.StatusNotFound, "ui_not_found", "admin UI is not embedded in this build", nil)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/_/")
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == "" || rel == "." {
		rel = "index.html"
	}
	serve := func(name string) bool {
		f, err := s.ui.Open(name)
		if err != nil {
			return false
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			return false
		}
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Cache-Control", "no-cache")
		if rs, ok := f.(io.ReadSeeker); ok {
			http.ServeContent(w, r, name, st.ModTime(), rs)
			return true
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return false
		}
		http.ServeContent(w, r, name, st.ModTime(), bytes.NewReader(data))
		return true
	}
	if serve(rel) {
		return
	}
	// SPA fallback for client-side routes, but not for missing assets.
	if !strings.Contains(path.Base(rel), ".") && serve("index.html") {
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "file not found", nil)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	cols := s.store.CollectionCount()
	uptime := int64(time.Since(s.started).Seconds())
	size := s.store.Size()
	clients := 0
	if s.hub != nil {
		clients = s.hub.ClientCount()
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "# HELP doketfire_uptime_seconds Process uptime in seconds.\n")
	fmt.Fprintf(w, "# TYPE doketfire_uptime_seconds counter\n")
	fmt.Fprintf(w, "doketfire_uptime_seconds %d\n", uptime)
	fmt.Fprintf(w, "# HELP doketfire_db_size_bytes Total size of the database file in bytes.\n")
	fmt.Fprintf(w, "# TYPE doketfire_db_size_bytes gauge\n")
	fmt.Fprintf(w, "doketfire_db_size_bytes %d\n", size)
	fmt.Fprintf(w, "# HELP doketfire_collections_total Total number of collections.\n")
	fmt.Fprintf(w, "# TYPE doketfire_collections_total gauge\n")
	fmt.Fprintf(w, "doketfire_collections_total %d\n", cols)
	fmt.Fprintf(w, "# HELP doketfire_realtime_clients Number of connected realtime SSE clients.\n")
	fmt.Fprintf(w, "# TYPE doketfire_realtime_clients gauge\n")
	fmt.Fprintf(w, "doketfire_realtime_clients %d\n", clients)
	fmt.Fprintf(w, "# HELP doketfire_info Build and version metadata.\n")
	fmt.Fprintf(w, "# TYPE doketfire_info gauge\n")
	fmt.Fprintf(w, "doketfire_info{version=%q} 1\n", s.version)
}
