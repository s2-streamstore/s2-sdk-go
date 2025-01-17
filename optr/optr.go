// Package optr provides utility functions for working with optional values
// represented as pointers in Go. It simplifies common operations like mapping,
// cloning, and handling default values.
package optr

// Some returns a pointer to the given value v. This function is useful for
// creating optional values from non-pointer types.
//
// Example:
//
//	value := Some(42)
//	fmt.Println(*value) // Output: 42
func Some[T any](v T) *T {
	return &v
}

// Cloned creates a new pointer by copying the value pointed to by v. If v is
// nil, it returns nil.
//
// Example:
//
//	original := Some(42)
//	copy := Cloned(original)
//	fmt.Println(*copy) // Output: 42
func Cloned[T any](v *T) *T {
	return Map(v, func(t T) T { return t })
}

// Map applies the function f to the value pointed to by v and returns a pointer
// to the result. If v is nil, it returns nil.
//
// Example:
//
//	value := Some(42)
//	result := Map(value, func(v int) string { return fmt.Sprintf("Value: %d", v) })
//	fmt.Println(*result) // Output: "Value: 42"
func Map[T, R any](v *T, f func(T) R) *R {
	return And(v, func(t T) (R, bool) { return f(t), true })
}

// TryMap applies the function f to the value pointed to by v and returns a
// pointer to the result or an error. If v is nil, it returns nil.
//
// Example:
//
//	value := Some(42)
//	result, err := TryMap(value, func(v int) (string, error) {
//	    return fmt.Sprintf("Value: %d", v), nil
//	})
//	if err != nil {
//	    fmt.Println("Error:", err)
//	} else {
//	    fmt.Println(*result) // Output: "Value: 42"
//	}
func TryMap[T, R any](v *T, f func(T) (R, error)) (*R, error) {
	return AndTry(v, func(t T) (R, bool, error) {
		r, err := f(t)

		return r, true, err
	})
}

// And applies the function f to the value pointed to by v if v is not nil. If f
// returns false, it returns nil. Otherwise, it returns a pointer to the result.
//
// Example:
//
//	value := Some(42)
//	result := And(value, func(v int) (string, bool) {
//	    if v > 40 {
//	        return "Greater than 40", true
//	    }
//	    return "", false
//	})
//	fmt.Println(*result) // Output: "Greater than 40"
func And[T, R any](v *T, f func(T) (R, bool)) *R {
	if v == nil {
		return nil
	}

	r, ok := f(*v)
	if !ok {
		return nil
	}

	return &r
}

// TryAnd applies the function f to the value pointed to by v if v is not nil.
// If f returns false or an error, it returns (nil, error). Otherwise, it returns
// a pointer to the result and a nil error.
//
// Example:
//
//	value := Some(42)
//	result, err := TryAnd(value, func(v int) (string, bool, error) {
//	    if v > 40 {
//	        return "Greater than 40", true, nil
//	    }
//	    return "", false, fmt.Errorf("value is not greater than 40")
//	})
//	if err != nil {
//	    fmt.Println("Error:", err)
//	} else if result != nil {
//	    fmt.Println(*result) // Output: "Greater than 40"
//	}
func AndTry[T, R any](v *T, f func(T) (R, bool, error)) (*R, error) {
	if v == nil {
		return nil, nil //nolint:nilnil
	}

	r, ok, err := f(*v)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, nil //nolint:nilnil
	}

	return &r, nil
}

// Or returns v if it is not nil. Otherwise, it calls f and returns a pointer to
// its result. If f returns false, it returns nil.
//
// Example:
//
//	value := Or(nil, func() (int, bool) {
//	    return 42, true
//	})
//	fmt.Println(*value) // Output: 42
func Or[T any](v *T, f func() (T, bool)) *T {
	if v != nil {
		return v
	}

	r, ok := f()
	if !ok {
		return nil
	}

	return &r
}

// OrTry returns v if it is not nil. Otherwise, it calls f and returns a pointer to
// its result along with an error, if any. If f returns false or an error, it returns
// nil and the error (if any).
//
// Example:
//
//	value, err := OrTry(nil, func() (int, bool, error) {
//	    return 42, true, nil
//	})
//	if err != nil {
//	    fmt.Println("Error:", err)
//	} else if value != nil {
//	    fmt.Println(*value) // Output: 42
//	} else {
//	    fmt.Println("No value")
//	}
func OrTry[T any](v *T, f func() (T, bool, error)) (*T, error) {
	if v != nil {
		return v, nil
	}

	r, ok, err := f()
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, nil //nolint:nilnil
	}

	return &r, nil
}

// UnwrapOr returns the value pointed to by v if v is not nil. Otherwise, it
// calls f and returns its result.
//
// Example:
//
//	value := UnwrapOr(nil, func() int {
//	    return 42
//	})
//	fmt.Println(value) // Output: 42
func UnwrapOr[T any](v *T, f func() T) T {
	if v != nil {
		return *v
	}

	return f()
}

// UnwrapOrTry returns the value pointed to by v if v is not nil. Otherwise, it
// calls f and returns its result along with any error. If f returns an error,
// that error is returned.
//
// Example:
//
//	value, err := UnwrapOrTry(nil, func() (int, error) {
//	    return 42, nil
//	})
//	if err != nil {
//	    fmt.Println("Error:", err)
//	} else {
//	    fmt.Println(value) // Output: 42
//	}
func UnwrapOrTry[T any](v *T, f func() (T, error)) (T, error) {
	if v != nil {
		return *v, nil
	}

	return f()
}
