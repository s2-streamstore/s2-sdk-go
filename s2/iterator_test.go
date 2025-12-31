package s2

import (
	"context"
	"testing"
)

func TestPager_EmptyPage(t *testing.T) {
	ctx := context.TODO()

	fetch := func(ctx context.Context, startAfter string) (*pagedResponse[AccessTokenInfo], error) {
		return &pagedResponse[AccessTokenInfo]{}, nil
	}

	page := newPager(ctx, fetch)

	if page.Next() != false {
		t.Fatalf("iterator should be empty")
	}
}

func TestPager_StopsAfterDone(t *testing.T) {
	calls := 0

	fetch := func(ctx context.Context, startAfter string) (*pagedResponse[int], error) {
		calls++
		if calls == 1 {
			return &pagedResponse[int]{
				items:   []int{42},
				nextKey: "",
			}, nil
		}

		t.Fatalf("fetch called after pager marked done")
		return nil, nil
	}

	p := newPager(context.Background(), fetch)

	if !p.Next() {
		t.Fatalf("expected one value")
	}
	if p.Value() != 42 {
		t.Fatalf("expected 42, got %v", p.Value())
	}

	if p.Next() {
		t.Fatalf("Next() should be false after done")
	}
}
