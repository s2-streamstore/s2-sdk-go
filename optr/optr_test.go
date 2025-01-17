package optr_test

import (
	"fmt"
	"testing"

	"github.com/s2-streamstore/s2-sdk-go/optr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptr_Some(t *testing.T) {
	t.Run("int", func(t *testing.T) {
		value := optr.Some(42)
		assert.NotNil(t, value)
		assert.Equal(t, 42, *value)
	})

	t.Run("uint", func(t *testing.T) {
		value := optr.Some(uint(42))
		assert.NotNil(t, value)
		assert.Equal(t, uint(42), *value)
	})

	t.Run("string", func(t *testing.T) {
		value := optr.Some("test")
		assert.NotNil(t, value)
		assert.Equal(t, "test", *value)
	})
}

func TestOptr_Cloned(t *testing.T) {
	t.Run("non-nil pointer", func(t *testing.T) {
		original := optr.Some(42)
		copied := optr.Cloned(original)

		require.NotNil(t, copied)
		assert.Equal(t, *original, *copied)
		assert.NotSame(t, original, copied)
	})

	t.Run("nil pointer", func(t *testing.T) {
		assert.Nil(t, optr.Cloned[int](nil))
	})
}

func TestOptr_Map(t *testing.T) {
	t.Run("non-nil value", func(t *testing.T) {
		value := optr.Some(42)
		result := optr.Map(value, func(v int) string { return fmt.Sprintf("Value: %d", v) })

		require.NotNil(t, result)
		assert.Equal(t, "Value: 42", *result)
	})

	t.Run("nil value", func(t *testing.T) {
		assert.Nil(t, optr.Map(nil, func(int) string { return "Should not happen" }))
	})
}

func TestOptr_TryMap(t *testing.T) {
	t.Run("non-nil value, no error", func(t *testing.T) {
		value := optr.Some(42)
		result, err := optr.TryMap(value, func(v int) (string, error) {
			return fmt.Sprintf("Value: %d", v), nil
		})

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "Value: 42", *result)
	})

	t.Run("non-nil value with error", func(t *testing.T) {
		value := optr.Some(42)
		result, err := optr.TryMap(value, func(int) (string, error) {
			return "", fmt.Errorf("error processing value")
		})

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, "error processing value", err.Error())
	})

	t.Run("nil value", func(t *testing.T) {
		result, err := optr.TryMap(nil, func(int) (string, error) {
			return "Should not happen", nil
		})

		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestOptr_And(t *testing.T) {
	t.Run("function returns true", func(t *testing.T) {
		value := optr.Some(42)
		result := optr.And(value, func(v int) (string, bool) {
			if v > 40 {
				return "Greater than 40", true
			}

			return "", false
		})

		require.NotNil(t, result)
		assert.Equal(t, "Greater than 40", *result)
	})

	t.Run("function returns false", func(t *testing.T) {
		value := optr.Some(42)
		assert.Nil(t, optr.And(value, func(int) (string, bool) { return "", false }))
	})

	t.Run("nil value", func(t *testing.T) {
		assert.Nil(t, optr.And(nil, func(int) (string, bool) { return "Should not happen", true }))
	})
}

func TestOptr_AndTry(t *testing.T) {
	t.Run("function returns true, no error", func(t *testing.T) {
		value := optr.Some(42)
		result, err := optr.AndTry(value, func(v int) (string, bool, error) {
			if v > 40 {
				return "Greater than 40", true, nil
			}

			return "", false, nil
		})

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "Greater than 40", *result)
	})

	t.Run("function returns false", func(t *testing.T) {
		value := optr.Some(42)
		result, err := optr.AndTry(value, func(int) (string, bool, error) {
			return "", false, nil
		})

		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("function returns an error", func(t *testing.T) {
		value := optr.Some(42)
		result, err := optr.AndTry(value, func(int) (string, bool, error) {
			return "", false, fmt.Errorf("error in function")
		})

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, "error in function", err.Error())
	})

	t.Run("nil value", func(t *testing.T) {
		result, err := optr.AndTry(nil, func(int) (string, bool, error) {
			return "Should not happen", true, nil
		})

		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestOptr_Or(t *testing.T) {
	t.Run("non-nil value", func(t *testing.T) {
		value := optr.Some(42)
		result := optr.Or(value, func() (int, bool) { return 0, true })

		require.NotNil(t, result)
		assert.Equal(t, 42, *result)
	})

	t.Run("nil value with true fallback", func(t *testing.T) {
		result := optr.Or(nil, func() (int, bool) { return 42, true })

		require.NotNil(t, result)
		assert.Equal(t, 42, *result)
	})

	t.Run("nil value with false fallback", func(t *testing.T) {
		assert.Nil(t, optr.Or(nil, func() (int, bool) { return 0, false }))
	})
}

func TestOptr_OrTry(t *testing.T) {
	t.Run("non-nil value", func(t *testing.T) {
		value := optr.Some(42)
		result, err := optr.OrTry(value, func() (int, bool, error) {
			return 0, true, nil
		})

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 42, *result)
	})

	t.Run("nil value with true fallback", func(t *testing.T) {
		result, err := optr.OrTry(nil, func() (int, bool, error) {
			return 42, true, nil
		})

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 42, *result)
	})

	t.Run("nil value with false fallback", func(t *testing.T) {
		result, err := optr.OrTry(nil, func() (int, bool, error) {
			return 0, false, nil
		})

		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("fallback returns an error", func(t *testing.T) {
		result, err := optr.OrTry(nil, func() (int, bool, error) {
			return 0, true, fmt.Errorf("error in fallback")
		})

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, "error in fallback", err.Error())
	})
}

func TestOptr_UnwrapOr(t *testing.T) {
	t.Run("non-nil value", func(t *testing.T) {
		value := optr.UnwrapOr(optr.Some(24), func() int { return 42 })
		assert.Equal(t, 24, value)
	})

	t.Run("nil value", func(t *testing.T) {
		value := optr.UnwrapOr(nil, func() int { return 42 })
		assert.Equal(t, 42, value)
	})
}

func TestOptr_UnwrapOrTry(t *testing.T) {
	t.Run("non-nil value", func(t *testing.T) {
		value, err := optr.UnwrapOrTry(optr.Some(24), func() (int, error) { return 42, nil })
		require.NoError(t, err)
		assert.Equal(t, 24, value)
	})

	t.Run("nil value with no error", func(t *testing.T) {
		value, err := optr.UnwrapOrTry(nil, func() (int, error) { return 42, nil })
		require.NoError(t, err)
		assert.Equal(t, 42, value)
	})

	t.Run("nil value with error", func(t *testing.T) {
		value, err := optr.UnwrapOrTry(nil, func() (int, error) { return 0, fmt.Errorf("error in fallback") })
		require.Error(t, err)
		assert.Equal(t, "error in fallback", err.Error())
		assert.Equal(t, 0, value)
	})
}
