package store

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/pj-1009/controlplane/internal/domain"
)

// DefaultLimit and MaxLimit bound list page sizes.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// Cursor marks the last row of a page. Lists are ordered by (created_at, id),
// so the next page starts strictly after this pair.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type cursorJSON struct {
	CreatedAt string    `json:"c"`
	ID        uuid.UUID `json:"i"`
}

// Encode returns the opaque cursor string handed to API clients.
func (c Cursor) Encode() string {
	// Full precision so the keyset comparison is exact; the API's millisecond
	// format would skip or repeat rows. Marshaling this struct cannot fail.
	b, _ := json.Marshal(cursorJSON{CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano), ID: c.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor produced by Encode.
func DecodeCursor(s string) (Cursor, error) {
	invalid := domain.NewValidationError("cursor", "is invalid")
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, invalid
	}
	var cj cursorJSON
	if err := json.Unmarshal(b, &cj); err != nil {
		return Cursor{}, invalid
	}
	t, err := time.Parse(time.RFC3339Nano, cj.CreatedAt)
	if err != nil || cj.ID == uuid.Nil {
		return Cursor{}, invalid
	}
	return Cursor{CreatedAt: t, ID: cj.ID}, nil
}

// Page is one page of a list result.
type Page[T any] struct {
	Items      []T
	NextCursor *Cursor
}

// paginate trims a limit+1 result to limit and computes the next cursor.
func paginate[T any](rows []T, limit int, key func(T) Cursor) Page[T] {
	if len(rows) <= limit {
		return Page[T]{Items: rows}
	}
	rows = rows[:limit]
	next := key(rows[len(rows)-1])
	return Page[T]{Items: rows, NextCursor: &next}
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	return min(limit, MaxLimit)
}
