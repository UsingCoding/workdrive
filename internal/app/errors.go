package app

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound    = errors.New("workspace not found")
	ErrConflict    = errors.New("workspace conflict")
	ErrUnsafe      = errors.New("operation blocked by safety checks")
	ErrNeedsTTY    = errors.New("this operation requires an interactive terminal")
	ErrInterrupted = errors.New("operation requires recovery")
)

// Blockers are displayed before an operation can modify a workspace.
type Blockers []string

func (b Blockers) Error() string {
	return fmt.Sprintf("%v: %s", ErrUnsafe, joinBlockers(b))
}

func (b Blockers) Unwrap() error { return ErrUnsafe }

func joinBlockers(items []string) string {
	if len(items) == 0 {
		return "unknown blocker"
	}
	result := items[0]
	for _, item := range items[1:] {
		result += "; " + item
	}
	return result
}
