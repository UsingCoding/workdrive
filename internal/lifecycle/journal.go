package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usingcoding/workdrive/internal/domain"
)

const journalVersion = 1

// Step records an intended side effect. CompletedAt is set only after the caller
// verifies the step's postcondition; on resume, inspect reality before retrying an
// uncompleted step because a crash may have occurred after the side effect.
type Step struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Operation is the durable record of a workspace mutation. All steps are written
// before the first external side effect; a journal is never proof of ownership.
type Operation struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	WorkspaceID   string            `json:"workspace_id"`
	Workspace     *domain.Workspace `json:"workspace,omitempty"`
	Kind          string            `json:"kind"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Steps         []Step            `json:"steps"`
	Error         string            `json:"error,omitempty"`
}

// Done reports whether every planned step was marked complete.
func (o *Operation) Done() bool {
	for _, step := range o.Steps {
		if step.CompletedAt == nil {
			return false
		}
	}
	return true
}

// Journal stores one fsynced JSON file per operation under Dir. Writers of the
// same workspace must hold its advisory lock throughout the operation.
type Journal struct{ Dir string }

// NewJournal creates a journal at an absolute directory path.
func NewJournal(dir string) (*Journal, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("journal directory must be absolute: %q", dir)
	}
	return &Journal{Dir: dir}, nil
}

// Plan persists an operation and all of its steps before execution. An empty
// step list is allowed for metadata-only operations.
func (j *Journal) Plan(workspaceID, kind string, steps []Step) (*Operation, error) {
	if workspaceID == "" || kind == "" {
		return nil, errors.New("workspace ID and operation kind are required")
	}
	seen := make(map[string]bool, len(steps))
	planned := make([]Step, len(steps))
	for i, step := range steps {
		if step.ID == "" || seen[step.ID] || step.CompletedAt != nil {
			return nil, fmt.Errorf("step %d must have a unique ID and be incomplete", i)
		}
		seen[step.ID] = true
		planned[i] = step
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	op := &Operation{SchemaVersion: journalVersion, ID: hex.EncodeToString(idBytes), WorkspaceID: workspaceID, Kind: kind, CreatedAt: now, UpdatedAt: now, Steps: planned}
	if err := j.write(op, true); err != nil {
		return nil, err
	}
	return op, nil
}

// PlanWorkspace records enough nonsecret workspace intent to recover a create
// interrupted before its first manifest or index entry could be written.
func (j *Journal) PlanWorkspace(w *domain.Workspace, kind string, steps []Step) (*Operation, error) {
	if w == nil || w.ID == "" || w.Key == "" || !filepath.IsAbs(w.Path) {
		return nil, errors.New("workspace identity and absolute path required")
	}
	op, err := j.Plan(w.ID, kind, steps)
	if err != nil {
		return nil, err
	}
	copy := *w
	copy.Resources = append([]domain.Resource(nil), w.Resources...)
	op.Workspace = &copy
	if err := j.write(op, false); err != nil {
		return nil, err
	}
	return op, nil
}

// Load reads a persisted operation without changing it.
func (j *Journal) Load(id string) (*Operation, error) {
	path, err := j.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var op Operation
	if err := json.Unmarshal(data, &op); err != nil {
		return nil, fmt.Errorf("decode operation %s: %w", id, err)
	}
	if op.SchemaVersion != journalVersion || op.ID != id {
		return nil, fmt.Errorf("invalid operation %s schema or ID", id)
	}
	return &op, nil
}

// Complete durably marks the named step complete. It is idempotent. The caller
// must verify the external postcondition first and hold the workspace lock.
func (j *Journal) Complete(id, stepID string) (*Operation, error) {
	op, err := j.Load(id)
	if err != nil {
		return nil, err
	}
	for i := range op.Steps {
		if op.Steps[i].ID != stepID {
			continue
		}
		if op.Steps[i].CompletedAt != nil {
			return op, nil
		}
		for _, previous := range op.Steps[:i] {
			if previous.CompletedAt == nil {
				return nil, fmt.Errorf("step %q cannot complete before %q", stepID, previous.ID)
			}
		}
		now := time.Now().UTC()
		op.Steps[i].CompletedAt = &now
		op.UpdatedAt = now
		op.Error = ""
		if err := j.write(op, false); err != nil {
			return nil, err
		}
		return op, nil
	}
	return nil, fmt.Errorf("unknown operation step %q", stepID)
}

// RecordError records a recoverable failure without losing planned steps.
func (j *Journal) RecordError(id string, cause error) (*Operation, error) {
	op, err := j.Load(id)
	if err != nil {
		return nil, err
	}
	if cause == nil {
		op.Error = ""
	} else {
		op.Error = cause.Error()
	}
	op.UpdatedAt = time.Now().UTC()
	if err := j.write(op, false); err != nil {
		return nil, err
	}
	return op, nil
}

func (j *Journal) path(id string) (string, error) {
	if j == nil || !filepath.IsAbs(j.Dir) || id == "" || strings.ContainsAny(id, "/\\.") {
		return "", fmt.Errorf("invalid journal directory or operation ID %q", id)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			if r < 'a' || r > 'f' {
				return "", fmt.Errorf("invalid operation ID %q", id)
			}
		}
	}
	return filepath.Join(j.Dir, id+".json"), nil
}

func (j *Journal) write(op *Operation, create bool) error {
	path, err := j.path(op.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(j.Dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(j.Dir, ".operation-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if create {
		// Operation IDs are 128-bit random values. Avoid ever exposing an empty
		// placeholder file: a crash before rename must leave no journal at all.
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("operation %s already exists", op.ID)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(j.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
