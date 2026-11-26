package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketRecords          = []byte("records")
	bucketPublishedCreated = []byte("idx_published_created")
	bucketSlug             = []byte("idx_slug")
)

// boltEngine stores JSON documents in Bbolt and maintains the same
// secondary indexes the v0.x implementation needs for list queries.
type boltEngine struct {
	db *bolt.DB
}

func newBoltEngine() Engine { return &boltEngine{} }

func (e *boltEngine) Name() string { return "bbolt" }

func (e *boltEngine) Open(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(dir, "app.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return err
	}
	e.db = db
	return db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketRecords, bucketPublishedCreated, bucketSlug} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e *boltEngine) Close() error {
	if e.db == nil {
		return nil
	}
	return e.db.Close()
}

func (e *boltEngine) Seed(ctx context.Context, records []Record, batch int) error {
	if batch <= 0 {
		batch = len(records)
	}
	for start := 0; start < len(records); start += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+batch, len(records))
		err := e.db.Update(func(tx *bolt.Tx) error {
			recs := tx.Bucket(bucketRecords)
			idx := tx.Bucket(bucketPublishedCreated)
			slugs := tx.Bucket(bucketSlug)
			for _, r := range records[start:end] {
				raw, err := json.Marshal(r)
				if err != nil {
					return err
				}
				if err := recs.Put([]byte(r.ID), raw); err != nil {
					return err
				}
				if err := slugs.Put([]byte(r.Slug), []byte(r.ID)); err != nil {
					return err
				}
				if r.Published {
					if err := idx.Put(publishedKey(r), nil); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func publishedKey(r Record) []byte {
	return []byte(fmt.Sprintf("1|%016d|%s", r.CreatedAt, r.ID))
}

func (e *boltEngine) PointGet(ctx context.Context, id string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	var out Record
	err := e.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketRecords).Get([]byte(id))
		if raw == nil {
			return fmt.Errorf("record %q not found", id)
		}
		return json.Unmarshal(raw, &out)
	})
	return out, err
}

func (e *boltEngine) ListPublished(ctx context.Context, limit int, beforeCreatedAt int64) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Record, 0, limit)
	err := e.db.View(func(tx *bolt.Tx) error {
		idx := tx.Bucket(bucketPublishedCreated)
		recs := tx.Bucket(bucketRecords)
		c := idx.Cursor()

		var k []byte
		if beforeCreatedAt == math.MaxInt64 {
			k, _ = c.Last()
		} else {
			seek := []byte(fmt.Sprintf("1|%016d|", beforeCreatedAt))
			k, _ = c.Seek(seek)
			if k == nil {
				k, _ = c.Last()
			} else {
				k, _ = c.Prev()
			}
		}

		for ; k != nil && len(out) < limit; k, _ = c.Prev() {
			key := string(k)
			if !strings.HasPrefix(key, "1|") {
				break
			}
			id := key[len("1|")+16+1:]
			raw := recs.Get([]byte(id))
			if raw == nil {
				continue
			}
			var r Record
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			out = append(out, r)
		}
		return nil
	})
	return out, err
}

func (e *boltEngine) CountPublished(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count := 0
	err := e.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketPublishedCreated).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if strings.HasPrefix(string(k), "1|") {
				count++
			}
		}
		return nil
	})
	return count, err
}

func (e *boltEngine) UpdateTitle(ctx context.Context, id, title string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.db.Update(func(tx *bolt.Tx) error {
		recs := tx.Bucket(bucketRecords)
		raw := recs.Get([]byte(id))
		if raw == nil {
			return fmt.Errorf("record %q not found", id)
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		r.Title = title
		r.UpdatedAt++
		updated, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return recs.Put([]byte(id), updated)
	})
}

func (e *boltEngine) DiskBytes() (int64, error) {
	var total int64
	err := filepath.WalkDir(filepath.Dir(e.db.Path()), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	if _, err := os.Stat(e.db.Path()); err != nil {
		return 0, err
	}
	return total, nil
}
