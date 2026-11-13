// Package domain defines persisted workspace and resource identities independently
// of their storage, command-line and UI implementations.
package domain

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const SchemaVersion = 1

// LifecycleState records the intended lifecycle; it does not imply that any
// directory or resource is actually present.
type LifecycleState string

const (
	StateCreating          LifecycleState = "creating"
	StateActive            LifecycleState = "active"
	StateParked            LifecycleState = "parked"
	StateCleaning          LifecycleState = "cleaning"
	StateAttentionRequired LifecycleState = "attention_required"
	StateRemoved           LifecycleState = "removed"
)

// ActualHealth describes the observed condition, separately from lifecycle.
type ActualHealth string

const (
	HealthUnknown  ActualHealth = "unknown"
	HealthHealthy  ActualHealth = "healthy"
	HealthMissing  ActualHealth = "missing"
	HealthConflict ActualHealth = "conflict"
	HealthError    ActualHealth = "error"
)

type Ownership string

const (
	OwnershipOwned   Ownership = "owned"
	OwnershipLinked  Ownership = "linked"
	OwnershipAdopted Ownership = "adopted"
)

// Workspace is the versioned, authoritative manifest model. Keys are unique
// case-sensitively; IDs are generated once and must not change on rename.
type Workspace struct {
	SchemaVersion int            `toml:"schema_version" json:"schema_version"`
	ID            string         `toml:"id" json:"id"`
	Key           string         `toml:"key" json:"key"`
	Name          string         `toml:"name" json:"name"`
	Path          string         `toml:"path" json:"path"`
	Profile       string         `toml:"profile" json:"profile"`
	State         LifecycleState `toml:"state" json:"state"`
	ActualStatus  ActualHealth   `toml:"actual_status,omitempty" json:"actual_status,omitempty"`
	CreatedAt     time.Time      `toml:"created_at" json:"created_at"`
	UpdatedAt     time.Time      `toml:"updated_at" json:"updated_at"`
	Resources     []Resource     `toml:"resources" json:"resources"`
}

// Resource describes a worktree in MVP 1 and leaves room for other resource
// types in later manifest versions. Source is linked; the worktree Path is
// owned only when ownership is explicitly "owned".
type Resource struct {
	ID           string       `toml:"id" json:"id"`
	Type         string       `toml:"type" json:"type"`
	Driver       string       `toml:"driver" json:"driver"`
	Ownership    Ownership    `toml:"ownership" json:"ownership"`
	Source       string       `toml:"source,omitempty" json:"source,omitempty"`
	Path         string       `toml:"path,omitempty" json:"path,omitempty"`
	Branch       string       `toml:"branch,omitempty" json:"branch,omitempty"`
	Base         string       `toml:"base,omitempty" json:"base,omitempty"`
	ActualStatus ActualHealth `toml:"actual_status,omitempty" json:"actual_status,omitempty"`
}

// NewID generates a stable, random RFC 4122 version 4 UUID.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate identity: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ValidateKey accepts portable filesystem names and rejects metadata names.
// Keys are compared case-sensitively by the registry.
func ValidateKey(key string) error {
	if key == "" || key == "." || key == ".." || strings.TrimSpace(key) != key {
		return fmt.Errorf("invalid workspace key %q: use a nonempty filesystem name without surrounding whitespace", key)
	}
	if key == ".workdrive" || key == ".git" {
		return fmt.Errorf("invalid workspace key %q: reserved metadata name", key)
	}
	for _, r := range key {
		if r == '_' || r == '-' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return fmt.Errorf("invalid workspace key %q: only letters, numbers, _, - and . are allowed", key)
	}
	return nil
}

// Transition validates and applies a lifecycle change. A transition to the
// same state is idempotent. Failure during provisioning or cleanup may enter
// attention_required; retrying from there requires an explicit target.
func (w *Workspace) Transition(next LifecycleState, now time.Time) error {
	if w == nil {
		return fmt.Errorf("nil workspace")
	}
	if w.State == next {
		return nil
	}
	allowed := map[LifecycleState][]LifecycleState{
		"":                     {StateCreating},
		StateCreating:          {StateActive, StateAttentionRequired},
		StateActive:            {StateParked, StateCleaning},
		StateParked:            {StateActive, StateCleaning},
		StateCleaning:          {StateRemoved, StateAttentionRequired},
		StateAttentionRequired: {StateCreating, StateCleaning},
	}
	for _, target := range allowed[w.State] {
		if next == target {
			w.State = next
			w.UpdatedAt = now
			return nil
		}
	}
	return fmt.Errorf("cannot transition workspace from %q to %q", w.State, next)
}

// NewWorkspace validates the key and assigns an identity before provisioning.
// Its paths and resource ownership must still be validated by the caller.
func NewWorkspace(key, name, path, profile string, now time.Time) (*Workspace, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	return &Workspace{
		SchemaVersion: SchemaVersion, ID: id, Key: key, Name: name,
		Path: path, Profile: profile, State: StateCreating,
		ActualStatus: HealthUnknown, CreatedAt: now, UpdatedAt: now,
		Resources: []Resource{},
	}, nil
}
