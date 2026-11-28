package httpapi

import (
	"errors"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"

	"github.com/a-jay98/stockpot/internal/media"
)

// BodyLimits maps an operation id to its x-max-body-bytes, for the few
// operations (uploads) allowed more than maxBodyBytes.
type BodyLimits map[string]int64

// ParseBodyLimits reads x-max-body-bytes from every operation that has it.
func ParseBodyLimits(spec *openapi3.T) (BodyLimits, error) {
	out := BodyLimits{}
	var errs []error
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			raw, ok := op.Extensions["x-max-body-bytes"]
			if !ok {
				continue
			}
			// YAML numbers decode as float64.
			n, ok := raw.(float64)
			if !ok || n < 1 || n != float64(int64(n)) {
				errs = append(errs, fmt.Errorf("%s %s: x-max-body-bytes must be a positive integer", method, path))
				continue
			}
			out[op.OperationID] = int64(n)
		}
	}
	return out, errors.Join(errs...)
}

func init() {
	// Image uploads are raw bytes; the validator only checks the media type
	// (the media service decodes and verifies the content).
	for _, ct := range media.ContentTypes {
		openapi3filter.RegisterBodyDecoder(ct, openapi3filter.FileBodyDecoder)
	}
}
