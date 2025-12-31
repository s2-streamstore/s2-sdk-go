package s2

import (
	"context"
)

const maxListPageSize = 1000

func copyLimit(limit *int) *int {
	if limit == nil {
		return nil
	}
	val := *limit
	return &val
}

func remainingDepleted(remaining *int) bool {
	return remaining != nil && *remaining <= 0
}

func nextRequestLimit(remaining *int) (int, bool) {
	if remaining == nil {
		return 0, false
	}
	if *remaining <= 0 {
		return 0, false
	}
	limit := *remaining
	if limit > maxListPageSize {
		limit = maxListPageSize
	}
	return limit, true
}

func consumeRemaining(remaining *int, consumed int) bool {
	if remaining == nil {
		return false
	}
	*remaining -= consumed
	return *remaining <= 0
}

type pager[T any] struct {
	ctx        context.Context
	fetch      func(context.Context, string) (*pagedResponse[T], error)
	startAfter string
	done       bool
	values     []T
	err        error
}

type pagedResponse[T any] struct {
	items   []T
	nextKey string
}

func newPager[T any](ctx context.Context, fetch func(context.Context, string) (*pagedResponse[T], error)) *pager[T] {
	if ctx == nil {
		ctx = context.Background()
	}
	return &pager[T]{ctx: ctx, fetch: fetch}
}

func (p *pager[T]) Next() bool {
	if p.done && len(p.values) == 0 {
		return false
	}
	// when the first call happens, the len(p.values) == 0
	if len(p.values) > 0 {
		p.values = p.values[1:]
	}
	for len(p.values) == 0 && !p.done && p.err == nil {
		var resp *pagedResponse[T]
		resp, p.err = p.fetch(p.ctx, p.startAfter)
		if p.err != nil {
			return false
		}
		p.values = resp.items
		if resp.nextKey == "" {
			p.done = true
		} else {
			p.startAfter = resp.nextKey
		}
	}
	return len(p.values) > 0
}

func (p *pager[T]) Value() T {
	// shouldnt be called when len is 0, but for safety..
	if len(p.values) == 0 {
		var zero T
		return zero
	}
	return p.values[0]
}

// Error while fetching a page.
func (p *pager[T]) Err() error {
	return p.err
}
