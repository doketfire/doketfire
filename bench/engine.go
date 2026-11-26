package main

import "context"

// Engine is the contract every storage backend must satisfy for the
// benchmark. Workloads are deliberately the same for all engines:
//
//   - point read by primary key
//   - list published records ordered by created_at DESC with a cursor
//   - count published records
//   - update a record's title by primary key
type Engine interface {
	Name() string
	Open(ctx context.Context, dir string) error
	Close() error
	Seed(ctx context.Context, records []Record, batch int) error
	PointGet(ctx context.Context, id string) (Record, error)
	ListPublished(ctx context.Context, limit int, beforeCreatedAt int64) ([]Record, error)
	CountPublished(ctx context.Context) (int, error)
	UpdateTitle(ctx context.Context, id, title string) error
	DiskBytes() (int64, error)
}
