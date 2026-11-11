package none

import (
	"context"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
)

type resource struct{}

func (resource) Create(context.Context, adapterpkg.Target) error  { return nil }
func (resource) Destroy(context.Context, adapterpkg.Target) error { return nil }
func New() adapterpkg.Definition {
	return adapterpkg.Definition{Name: "none", Adapter: resource{}, CreateAllowed: true,
		Prepare: func(_ context.Context, _ adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
			return adapterpkg.ResourceRef{Adapter: "none"}, nil
		},
		Validate: func(string, adapterpkg.Metadata, adapterpkg.ResourceRef) error { return nil },
		Env:      func(adapterpkg.ResourceRef) adapterpkg.Environment { return adapterpkg.Environment{} },
	}
}
