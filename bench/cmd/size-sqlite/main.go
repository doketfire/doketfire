// Command size-sqlite builds a minimal binary that only links the
// modernc.org/sqlite engine. It exists to measure the dependency cost of
// the storage layer.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func main() {
	dir, err := os.MkdirTemp("", "doketfire-size-sqlite-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "app.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		panic(err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY) STRICT`); err != nil {
		panic(err)
	}
	fmt.Println("ok")
}
