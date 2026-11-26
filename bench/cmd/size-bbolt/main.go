// Command size-bbolt builds a minimal binary that only links the Bbolt
// engine. It exists to measure the dependency cost of the storage layer.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

func main() {
	dir, err := os.MkdirTemp("", "doketfire-size-bbolt-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	db, err := bolt.Open(filepath.Join(dir, "app.db"), 0o600, nil)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("records"))
		return err
	}); err != nil {
		panic(err)
	}
	fmt.Println("ok")
}
