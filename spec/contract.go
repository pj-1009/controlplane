package spec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// Contract checks HTTP responses against openapi.yaml. Only tests use it.
type Contract struct {
	doc    *openapi3.T
	router routers.Router
}

// Load parses and validates openapi.yaml.
func Load() (*Contract, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(OpenAPI)
	if err != nil {
		return nil, fmt.Errorf("parse openapi.yaml: %w", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("openapi.yaml is not a valid OpenAPI document: %w", err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build router from openapi.yaml: %w", err)
	}
	return &Contract{doc: doc, router: router}, nil
}

// Operations returns every documented "METHOD /path", for example
// "PATCH /tenants/{id}".
func (c *Contract) Operations() []string {
	var ops []string
	for path, item := range c.doc.Paths.Map() {
		for method := range item.Operations() {
			ops = append(ops, method+" "+path)
		}
	}
	return ops
}

// ValidateResponse checks that the response to req is documented: its
// status code, headers and body must match the spec. A request to a route
// the spec doesn't have is accepted only if the response is the router's
// own 404 or 405.
func (c *Contract) ValidateResponse(req *http.Request, status int, header http.Header, body []byte) error {
	route, pathParams, err := c.router.FindRoute(req)
	if err != nil {
		if (errors.Is(err, routers.ErrPathNotFound) && status == http.StatusNotFound) ||
			(errors.Is(err, routers.ErrMethodNotAllowed) && status == http.StatusMethodNotAllowed) {
			return nil
		}
		return fmt.Errorf("%s %s is not in openapi.yaml: %w", req.Method, req.URL.Path, err)
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
		},
		Status: status,
		Header: header,
		Body:   io.NopCloser(bytes.NewReader(body)),
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true, // an undocumented status code is an error
			MultiError:            true,
		},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		return fmt.Errorf("%s %s returned %d, which doesn't match openapi.yaml: %w", req.Method, req.URL.Path, status, err)
	}
	return nil
}
