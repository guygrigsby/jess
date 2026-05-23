package jess

import (
	"errors"
	"fmt"
)

// Sentinel errors callers can match with errors.Is. Kept in one place so
// the public surface is easy to audit and stays consistent — every
// publicly-returned error from jess either is one of these, wraps one
// of these, or is a typed error with its own godoc.
var (
	// errNilProvider is returned when a nil Provider is passed to
	// NewProviderRegistry. Wrapped as a sentinel so callers can match
	// even if message text changes.
	errNilProvider = errors.New("jess: nil Provider")

	// errEmptyProviderName is returned when a Provider's Name() is
	// empty at registry construction. Empty names would silently match
	// the empty ModelID.Provider() result; rejecting up-front is safer.
	errEmptyProviderName = errors.New("jess: Provider has empty Name()")
)

// duplicateProviderErr is returned when two Providers share a Name() in
// NewProviderRegistry. Typed so the offending name can be surfaced
// without string parsing.
type duplicateProviderErr struct {
	Name string
}

func (e *duplicateProviderErr) Error() string {
	return fmt.Sprintf("jess: duplicate Provider name %q", e.Name)
}
