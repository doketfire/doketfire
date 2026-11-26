// Package files stores uploaded files on the local filesystem. The interface
// is intentionally small so an S3-compatible backend can be added later.
package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/doketfire/doketfire/internal/collections"
)

var (
	ErrTooLarge    = errors.New("file exceeds the maximum allowed size")
	ErrInvalidName = errors.New("invalid file name")
	ErrNotFound    = errors.New("file not found")
	ErrInvalidPath = errors.New("invalid file path")
)

// Store saves files under root/<collection>/<document id>/<stored name>.
type Store struct {
	root    string
	maxSize int64
}

// File describes a stored file.
type File struct {
	Collection   string `json:"collection"`
	DocumentID   string `json:"documentId"`
	Name         string `json:"name"`         // generated, URL-safe name
	OriginalName string `json:"originalName"` // sanitized client name
	Size         int64  `json:"size"`
	ContentType  string `json:"contentType"`
}

// New creates a local file store.
func New(root string, maxSize int64) (*Store, error) {
	if root == "" {
		return nil, errors.New("files: root directory is required")
	}
	if maxSize <= 0 {
		maxSize = 25 << 20 // 25 MiB
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create files directory: %w", err)
	}
	return &Store{root: root, maxSize: maxSize}, nil
}

// Root returns the storage root.
func (s *Store) Root() string { return s.root }

// MaxSize returns the configured per-file limit.
func (s *Store) MaxSize() int64 { return s.maxSize }

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SanitizeName converts a client filename into a safe base name.
func SanitizeName(name string) (string, error) {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	name = unsafeChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-")
	if name == "" || name == "." || name == ".." {
		return "", ErrInvalidName
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:200-len(ext)] + ext
	}
	return name, nil
}

// Save stores a file. The returned Name is the generated stored name.
func (s *Store) Save(collection, docID, filename string, r io.Reader) (*File, error) {
	dir, err := s.docDir(collection, docID)
	if err != nil {
		return nil, err
	}
	original, err := SanitizeName(filename)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r, s.maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.maxSize {
		return nil, ErrTooLarge
	}
	ext := strings.ToLower(filepath.Ext(original))
	if len(ext) > 20 {
		ext = ""
	}
	base := strings.TrimSuffix(original, filepath.Ext(original))
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	stored := base + "-" + hex.EncodeToString(suffix) + ext
	full := filepath.Join(dir, stored)
	if !within(s.root, full) {
		return nil, ErrInvalidPath
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Write to a temporary file and rename so readers never see partial files.
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, full); err != nil {
		return nil, err
	}
	ctype := http.DetectContentType(data)
	if byExt := mime.TypeByExtension(ext); byExt != "" {
		ctype = byExt
	}
	return &File{
		Collection:   collection,
		DocumentID:   docID,
		Name:         stored,
		OriginalName: original,
		Size:         int64(len(data)),
		ContentType:  ctype,
	}, nil
}

// Open opens a stored file for reading.
func (s *Store) Open(collection, docID, name string) (*os.File, string, error) {
	dir, err := s.docDir(collection, docID)
	if err != nil {
		return nil, "", err
	}
	name = filepath.Base(name)
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return nil, "", ErrInvalidName
	}
	full := filepath.Join(dir, name)
	if !within(s.root, full) {
		return nil, "", ErrInvalidPath
	}
	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return f, ctype, nil
}

// Delete removes one stored file.
func (s *Store) Delete(collection, docID, name string) error {
	dir, err := s.docDir(collection, docID)
	if err != nil {
		return err
	}
	name = filepath.Base(name)
	full := filepath.Join(dir, name)
	if !within(s.root, full) {
		return ErrInvalidPath
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteDocument removes every file belonging to a document.
func (s *Store) DeleteDocument(collection, docID string) error {
	dir, err := s.docDir(collection, docID)
	if err != nil {
		return err
	}
	if !within(s.root, dir) {
		return ErrInvalidPath
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteCollection removes every file belonging to a collection.
func (s *Store) DeleteCollection(collection string) error {
	if !collections.ValidName(collection) {
		return ErrInvalidPath
	}
	dir := filepath.Join(s.root, collection)
	if !within(s.root, dir) {
		return ErrInvalidPath
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) docDir(collection, docID string) (string, error) {
	if !collections.ValidName(collection) {
		return "", ErrInvalidPath
	}
	if !collections.ValidID(docID) {
		return "", ErrInvalidPath
	}
	dir := filepath.Join(s.root, collection, docID)
	if !within(s.root, dir) {
		return "", ErrInvalidPath
	}
	return dir, nil
}

func within(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
