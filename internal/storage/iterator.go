package storage

import (
	"bytes"
	"fmt"

	"github.com/doketfire/doketfire/internal/collections"
	bolt "go.etcd.io/bbolt"
)

// boltIterator walks either a documents bucket or an index bucket.
type boltIterator struct {
	cursor  *bolt.Cursor
	docs    *bolt.Bucket
	indexed bool
	opts    ScanOpts
	start   []byte
	end     []byte
	reverse bool

	started  bool
	finished bool
	yielded  int
	key      []byte
	doc      Doc
	err      error
}

func newIterator(cursor *bolt.Cursor, docs *bolt.Bucket, indexed bool, r KeyRange, opts ScanOpts) *boltIterator {
	return &boltIterator{
		cursor:  cursor,
		docs:    docs,
		indexed: indexed,
		opts:    opts,
		start:   r.Start,
		end:     r.End,
		reverse: opts.Reverse,
	}
}

func emptyIterator() Iterator { return &boltIterator{started: true, finished: true} }

// Next advances to the next document that satisfies the range and cursor.
func (it *boltIterator) Next() bool {
	if it.finished || it.cursor == nil {
		it.finished = true
		return false
	}
	for {
		var k, v []byte
		if !it.started {
			it.started = true
			k, v = it.position()
		} else if it.reverse {
			k, v = it.cursor.Prev()
		} else {
			k, v = it.cursor.Next()
		}
		if k == nil {
			it.finished = true
			return false
		}
		if !it.inBounds(k) {
			it.finished = true
			return false
		}
		doc, err := it.load(k, v)
		if err != nil {
			it.err = err
			it.finished = true
			return false
		}
		it.yielded++
		if it.opts.Limit > 0 && it.yielded > it.opts.Limit {
			it.finished = true
			return false
		}
		it.key = append(it.key[:0], k...)
		it.doc = doc
		return true
	}
}

func (it *boltIterator) position() (k, v []byte) {
	c := it.cursor
	if it.reverse {
		if len(it.opts.After) > 0 {
			k, v = c.Seek(it.opts.After)
			if k == nil {
				k, v = c.Last()
			} else {
				k, v = c.Prev()
			}
		} else {
			k, v = c.Last()
		}
		// Skip entries above the exclusive upper bound.
		for k != nil && len(it.end) > 0 && bytes.Compare(k, it.end) >= 0 {
			k, v = c.Prev()
		}
		return k, v
	}
	if len(it.opts.After) > 0 {
		k, v = c.Seek(it.opts.After)
		if k != nil && bytes.Equal(k, it.opts.After) {
			k, v = c.Next()
		}
	} else if len(it.start) > 0 {
		k, v = c.Seek(it.start)
	} else {
		k, v = c.First()
	}
	for k != nil && len(it.start) > 0 && bytes.Compare(k, it.start) < 0 {
		k, v = c.Next()
	}
	return k, v
}

func (it *boltIterator) inBounds(k []byte) bool {
	if it.reverse {
		if len(it.start) > 0 && bytes.Compare(k, it.start) < 0 {
			return false
		}
		if len(it.end) > 0 && bytes.Compare(k, it.end) >= 0 {
			return false
		}
		return true
	}
	if len(it.start) > 0 && bytes.Compare(k, it.start) < 0 {
		return false
	}
	if len(it.end) > 0 && bytes.Compare(k, it.end) >= 0 {
		return false
	}
	return true
}

func (it *boltIterator) load(key, value []byte) (Doc, error) {
	if !it.indexed {
		d, err := collections.Decode(value)
		if err != nil {
			return nil, fmt.Errorf("decode document %q: %w", key, err)
		}
		return d, nil
	}
	if len(value) != 16 {
		return nil, fmt.Errorf("index entry %q has invalid document id", key)
	}
	raw := it.docs.Get(value)
	if raw == nil {
		return nil, fmt.Errorf("index entry %q points to missing document", key)
	}
	d, err := collections.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("decode document referenced by index: %w", err)
	}
	return d, nil
}

func (it *boltIterator) Key() []byte { return it.key }
func (it *boltIterator) Doc() Doc    { return it.doc }
func (it *boltIterator) Err() error  { return it.err }
func (it *boltIterator) Close() error {
	it.finished = true
	return nil
}
