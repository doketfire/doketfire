package files

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doketfire/doketfire/internal/collections"
)

func TestSaveOpenDelete(t *testing.T) {
	store, err := New(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	id := collections.NewID()
	data := []byte("hello file")
	f, err := store.Save("posts", id, "My Photo.PNG", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.Name, "My-Photo-") || !strings.HasSuffix(f.Name, ".png") {
		t.Fatalf("unexpected stored name: %s", f.Name)
	}
	if f.Size != int64(len(data)) || f.ContentType == "" {
		t.Fatalf("unexpected metadata: %+v", f)
	}
	file, _, err := store.Open("posts", id, f.Name)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(file)
	file.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q", got)
	}
	if _, _, err := store.Open("posts", id, "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := store.Delete("posts", id, f.Name); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open("posts", id, f.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted file, got %v", err)
	}
}

func TestLimitsAndValidation(t *testing.T) {
	store, err := New(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	id := collections.NewID()
	if _, err := store.Save("posts", id, "big.txt", strings.NewReader("123456789")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected too large, got %v", err)
	}
	if _, err := store.Save("../bad", id, "x.txt", strings.NewReader("x")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected invalid collection, got %v", err)
	}
	if _, err := store.Save("posts", "not-a-ulid", "x.txt", strings.NewReader("x")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected invalid doc id, got %v", err)
	}
}

func TestSanitizeNamePreventsTraversal(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":                "passwd",
		`..\..\windows\system32\evil.dll`: "evil.dll",
		"normal.txt":                      "normal.txt",
		"a b c?.txt":                      "a-b-c-.txt",
	}
	for in, want := range cases {
		got, err := SanitizeName(in)
		if err != nil {
			t.Fatalf("SanitizeName(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", ".", "..", "///"} {
		if _, err := SanitizeName(bad); err == nil {
			t.Errorf("SanitizeName(%q) should fail", bad)
		}
	}
}

func TestDeleteDocument(t *testing.T) {
	root := t.TempDir()
	store, _ := New(root, 1024)
	id := collections.NewID()
	f, err := store.Save("posts", id, "a.txt", strings.NewReader("a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "posts", id, f.Name)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDocument("posts", id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "posts", id)); !os.IsNotExist(err) {
		t.Fatalf("directory still exists: %v", err)
	}
}

func TestOpenRejectsTraversal(t *testing.T) {
	store, _ := New(t.TempDir(), 1024)
	id := collections.NewID()
	if _, _, err := store.Open("posts", id, ".."); err == nil {
		t.Fatal("expected error for ..")
	}
	if _, _, err := store.Open("posts", id, "../../data.db"); err == nil {
		t.Fatal("expected error for traversal")
	}
}
