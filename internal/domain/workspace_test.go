package domain

import (
	"regexp"
	"testing"
	"time"
)

func TestKeyValidation(t *testing.T) {
	for _, key := range []string{"PLATFORM-1425", "scratch.api", "équipe_1"} {
		if err := ValidateKey(key); err != nil {
			t.Errorf("valid key %q: %v", key, err)
		}
	}
	for _, key := range []string{"", ".", "..", ".workdrive", ".git", " ../escape", "a/b", `a\b`, " leading", "trailing ", "line\nfeed"} {
		if err := ValidateKey(key); err == nil {
			t.Errorf("accepted unsafe key %q", key)
		}
	}
}

func TestIdentityAndLifecycle(t *testing.T) {
	now := time.Now().UTC()
	w, err := NewWorkspace("scratch-api", "Scratch API", "/tmp/workspaces/scratch-api", "development", now)
	if err != nil {
		t.Fatal(err)
	}
	if w.SchemaVersion != 1 || w.State != StateCreating || w.ActualStatus != HealthUnknown || !w.CreatedAt.Equal(now) {
		t.Fatalf("unexpected new workspace: %+v", w)
	}
	validUUID := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !validUUID.MatchString(w.ID) {
		t.Fatalf("invalid UUID %q", w.ID)
	}
	if other, err := NewID(); err != nil || other == w.ID {
		t.Fatalf("nonunique ID %q: %v", other, err)
	}
	for _, state := range []LifecycleState{StateActive, StateParked, StateActive, StateCleaning, StateAttentionRequired, StateCleaning, StateRemoved} {
		if err := w.Transition(state, now.Add(time.Minute)); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}
	if err := w.Transition(StateActive, now.Add(time.Hour)); err == nil {
		t.Fatal("resurrected removed workspace")
	}
	if w.ActualStatus != HealthUnknown {
		t.Fatal("lifecycle changed observed health")
	}
}

func TestTransitionRejectsSkipAndIsIdempotent(t *testing.T) {
	w := &Workspace{State: StateCreating}
	if err := w.Transition(StateParked, time.Now()); err == nil {
		t.Fatal("creating workspace parked without completion")
	}
	if err := w.Transition(StateCreating, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := w.Transition(StateAttentionRequired, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := w.Transition(StateCreating, time.Now()); err != nil {
		t.Fatal(err)
	}
}
