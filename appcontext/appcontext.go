package appcontext

import (
	"github.com/PlakarKorp/kloset/kcontext"
)

type AppContext struct {
	*kcontext.KContext
}

func NewAppContext() *AppContext {
	return &AppContext{
		KContext: kcontext.NewKContext(),
	}
}

func NewAppContextFrom(ctx *AppContext) *AppContext {
	return &AppContext{
		KContext: kcontext.NewKContextFrom(ctx.GetInner()),
	}
}

// XXX: This needs to go away progressively by migrating to AppContext.
func (c *AppContext) GetInner() *kcontext.KContext {
	return c.KContext
}
