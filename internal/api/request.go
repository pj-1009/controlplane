package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/pj-1009/controlplane/internal/domain"
	"github.com/pj-1009/controlplane/internal/store"
)

const maxBodyBytes = 1 << 20

// decodeJSON strictly decodes a JSON object body into dst. Malformed JSON,
// unknown fields and trailing data are validation errors.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return domain.NewValidationError("body", bodyErrorMessage(err))
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.NewValidationError("body", "must contain a single JSON object")
	}
	return nil
}

func bodyErrorMessage(err error) string {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxErr *http.MaxBytesError
	switch {
	case errors.Is(err, io.EOF):
		return "must not be empty"
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return "is not valid JSON"
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return "field " + typeErr.Field + " has the wrong type"
		}
		return "must be a JSON object"
	case errors.As(err, &maxErr):
		return "is too large"
	default:
		// Covers `json: unknown field "x"`, which has no typed error.
		return err.Error()
	}
}

// listParams holds the shared pagination query parameters.
type listParams struct {
	limit int
	after *store.Cursor
}

// parseListParams reads ?limit= and ?cursor=, adding problems to fields.
func parseListParams(r *http.Request, fields map[string]string) listParams {
	q := r.URL.Query()
	p := listParams{limit: store.DefaultLimit}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxLimit {
			fields["limit"] = "must be an integer between 1 and " + strconv.Itoa(store.MaxLimit)
		} else {
			p.limit = n
		}
	}
	if raw := q.Get("cursor"); raw != "" {
		c, err := store.DecodeCursor(raw)
		if err != nil {
			fields["cursor"] = "is invalid"
		} else {
			p.after = &c
		}
	}
	return p
}

// parseQueryUUID reads an optional UUID query parameter.
func parseQueryUUID(r *http.Request, name string, fields map[string]string) uuid.UUID {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		fields[name] = "must be a UUID"
		return uuid.Nil
	}
	return id
}

// listResponse is the envelope for every list endpoint.
type listResponse[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func newListResponse[T any](page store.Page[T]) listResponse[T] {
	resp := listResponse[T]{Items: page.Items}
	if resp.Items == nil {
		resp.Items = []T{}
	}
	if page.NextCursor != nil {
		c := page.NextCursor.Encode()
		resp.NextCursor = &c
	}
	return resp
}

func validationFailed(fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	return &domain.ValidationError{Fields: fields}
}
