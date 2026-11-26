package main

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Record is the logical document used by every engine. It mirrors the
// shape of a blog post collection in the v1.0 schema.
type Record struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Slug      string `json:"slug"`
	Body      string `json:"body"`
	Published bool   `json:"published"`
	AuthorID  string `json:"author_id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

var titleWords = []string{
	"doketfire", "sqlite", "bbolt", "schema", "migration", "realtime",
	"authorization", "policy", "query", "storage", "backup", "replica",
	"stream", "cursor", "record", "collection", "binary", "latency",
}

var bodyWords = []string{
	"lorem", "ipsum", "dolor", "sit", "amet", "consectetur", "adipiscing",
	"elit", "sed", "do", "eiusmod", "tempor", "incididunt", "ut", "labore",
	"et", "dolore", "magna", "aliqua", "enim", "ad", "minim", "veniam",
}

// deterministicReader turns a math/rand source into the io.Reader that
// ulid.MustNew expects. The dataset must be identical on every run.
type deterministicReader struct{ r *rand.Rand }

func (d deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(d.r.Intn(256))
	}
	return len(p), nil
}

// GenerateDataset builds n records with the same seed and field sizes on
// every run. Timestamps are one second apart so cursor pagination is
// meaningful and IDs stay sortable.
func GenerateDataset(n int, seed int64) []Record {
	rng := rand.New(rand.NewSource(seed))
	entropy := deterministicReader{r: rng}
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	const authors = 50
	authorIDs := make([]string, authors)
	for i := range authorIDs {
		ts := base.Add(time.Duration(i) * time.Millisecond)
		authorIDs[i] = ulid.MustNew(uint64(ts.UnixMilli()), entropy).String()
	}

	records := make([]Record, n)
	for i := 0; i < n; i++ {
		created := base.Add(time.Duration(i) * time.Second)
		title := fmt.Sprintf("%s %s %d",
			titleWords[rng.Intn(len(titleWords))],
			titleWords[rng.Intn(len(titleWords))],
			i,
		)
		slug := fmt.Sprintf("%s-%s-%d",
			titleWords[rng.Intn(len(titleWords))],
			titleWords[rng.Intn(len(titleWords))],
			i,
		)
		body := buildBody(rng, i)
		records[i] = Record{
			ID:        ulid.MustNew(uint64(created.UnixMilli()), entropy).String(),
			Title:     title,
			Slug:      slug,
			Body:      body,
			Published: rng.Intn(100) < 60,
			AuthorID:  authorIDs[rng.Intn(authors)],
			CreatedAt: created.UnixMilli(),
			UpdatedAt: created.UnixMilli(),
		}
	}
	return records
}

func buildBody(rng *rand.Rand, i int) string {
	var b strings.Builder
	b.Grow(320)
	fmt.Fprintf(&b, "record %d. ", i)
	for j := 0; j < 40; j++ {
		b.WriteString(bodyWords[rng.Intn(len(bodyWords))])
		b.WriteByte(' ')
	}
	return b.String()
}
