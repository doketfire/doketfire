package api

import (
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/storage"
)

// handleUploadFile implements POST /api/collections/{col}/records/{id}/files.
// The multipart form must contain a "file" part. An optional "field" value
// stores the generated file name in that document field.
func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	col, docID := r.PathValue("col"), r.PathValue("id")
	def, doc, err := s.loadRecord(col, docID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	allowed, err := s.authorize(def, "update", identityFrom(r), doc, nil, nil)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "you cannot attach files to this record", nil)
		return
	}

	limit := s.cfg.MaxUploadBytes + (1 << 20) // multipart overhead
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded file is too large", nil)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_multipart", "expected a multipart/form-data body", nil)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	src, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_file", `the form must include a "file" part`, nil)
		return
	}
	defer src.Close()

	stored, err := s.files.Save(col, docID, header.Filename, src)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	field := strings.TrimSpace(r.FormValue("field"))
	if field != "" {
		if err := validFileField(field); err != nil {
			_ = s.files.Delete(col, docID, stored.Name)
			writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), nil)
			return
		}
		err := s.store.Update(func(tx storage.WriteTx) error {
			current, ok, err := tx.GetDoc(col, docID)
			if err != nil {
				return err
			}
			if !ok {
				return storage.ErrNotFound
			}
			allowed, err := s.authorize(def, "update", identityFrom(r), current, nil, nil)
			if err != nil {
				return err
			}
			if !allowed {
				return errForbidden("you cannot update this record")
			}
			collections.SetPath(current, field, stored.Name)
			return tx.PutDoc(col, current)
		})
		if err != nil {
			_ = s.files.Delete(col, docID, stored.Name)
			if errors.Is(err, errForbiddenSentinel) {
				writeError(w, http.StatusForbidden, "forbidden", "you cannot update this record", nil)
				return
			}
			s.writeServiceError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, stored)
}

// handleDeleteFile implements DELETE /api/collections/{col}/records/{id}/files/{name}.
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	col, docID, name := r.PathValue("col"), r.PathValue("id"), r.PathValue("name")
	def, doc, err := s.loadRecord(col, docID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	allowed, err := s.authorize(def, "update", identityFrom(r), doc, nil, nil)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "you cannot remove files from this record", nil)
		return
	}
	if err := s.files.Delete(col, docID, name); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if field := strings.TrimSpace(r.URL.Query().Get("field")); field != "" {
		if err := validFileField(field); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), nil)
			return
		}
		err := s.store.Update(func(tx storage.WriteTx) error {
			current, ok, err := tx.GetDoc(col, docID)
			if err != nil {
				return err
			}
			if !ok {
				return storage.ErrNotFound
			}
			collections.DeletePath(current, field)
			return tx.PutDoc(col, current)
		})
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDownloadFile implements GET /api/files/{col}/{id}/{name}.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	col, docID, name := r.PathValue("col"), r.PathValue("id"), r.PathValue("name")
	def, doc, err := s.loadRecord(col, docID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	allowed, err := s.authorize(def, "view", identityFrom(r), doc, nil, nil)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "you cannot read files from this record", nil)
		return
	}
	f, ctype, err := s.files.Open(col, docID, name)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	base := st.Name()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Security-Policy", "sandbox")
	if disp := mime.FormatMediaType("inline", map[string]string{"filename": base}); disp != "" {
		w.Header().Set("Content-Disposition", disp)
	}
	http.ServeContent(w, r, base, st.ModTime(), f)
}

// loadRecord loads a collection definition and one record in a single snapshot.
func (s *Server) loadRecord(col, docID string) (*collections.CollectionDef, collections.Document, error) {
	var (
		def *collections.CollectionDef
		doc collections.Document
	)
	err := s.store.View(func(tx storage.ReadTx) error {
		var err error
		def, err = tx.Collection(col)
		if err != nil {
			return err
		}
		var ok bool
		doc, ok, err = tx.GetDoc(col, docID)
		if err != nil {
			return err
		}
		if !ok {
			return storage.ErrNotFound
		}
		return nil
	})
	return def, doc, err
}

var fileReservedFields = map[string]bool{
	collections.FieldID:           true,
	collections.FieldCreated:      true,
	collections.FieldUpdated:      true,
	collections.FieldEmail:        true,
	collections.FieldPasswordHash: true,
	collections.FieldTokenKey:     true,
	collections.FieldVerified:     true,
}

func validFileField(field string) error {
	if !collections.ValidPath(field) {
		return errors.New("field must be a valid dot-separated path")
	}
	first := strings.SplitN(field, ".", 2)[0]
	if fileReservedFields[first] {
		return errors.New("field " + first + " is reserved and cannot store files")
	}
	return nil
}
