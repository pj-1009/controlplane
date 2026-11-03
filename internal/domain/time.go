package domain

import (
	"fmt"
	"time"
)

// TimeLayout is ISO 8601 with millisecond precision. Values are always
// formatted in UTC, so the zone renders as "Z".
const TimeLayout = "2006-01-02T15:04:05.000Z07:00"

// Timestamp is a time.Time that marshals to JSON using TimeLayout in UTC.
type Timestamp struct {
	time.Time
}

// NewTimestamp wraps t, normalized to UTC.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp{Time: t.UTC()}
}

// Now returns the current time as a Timestamp.
func Now() Timestamp {
	return NewTimestamp(time.Now())
}

// String formats the timestamp using TimeLayout.
func (t Timestamp) String() string {
	return t.UTC().Format(TimeLayout)
}

// MarshalJSON implements json.Marshaler.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.String() + `"`), nil
}

// UnmarshalJSON accepts any RFC 3339 timestamp, with or without fractional seconds.
func (t *Timestamp) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("timestamp must be a JSON string, got %s", data)
	}
	parsed, err := time.Parse(time.RFC3339Nano, string(data[1:len(data)-1]))
	if err != nil {
		return fmt.Errorf("parse timestamp: %w", err)
	}
	*t = NewTimestamp(parsed)
	return nil
}
