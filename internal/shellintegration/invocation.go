// Package shellintegration implements the opt-in, literal parent-shell handoff.
// It does not resolve workspaces or authorize navigation; callers must do that
// before publishing a destination.
package shellintegration

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/usingcoding/workdrive/internal/domain"
	"golang.org/x/sys/unix"
)

const (
	DirectiveEnv = "WORKDRIVE_DIRECTIVE_CD_FILE"
	PreviousEnv  = "WORKDRIVE_PREVIOUS_WORKSPACE"

	protocolVersion      = "1"
	identityPrefix       = "v1."
	maxIdentityTokenSize = 1 << 20
)

// Identity is a workspace identity, not a repository destination. A decoded
// identity remains untrusted until checked against the authoritative manifest.
type Identity struct{ ID, Key, Path string }

func (i Identity) validate() error {
	if i.ID == "" || strings.ContainsRune(i.ID, 0) {
		return errors.New("previous workspace has an invalid ID")
	}
	if err := domain.ValidateKey(i.Key); err != nil {
		return err
	}
	if !filepath.IsAbs(i.Path) || strings.ContainsRune(i.Path, 0) {
		return errors.New("previous workspace path must be absolute and contain no NUL")
	}
	return nil
}

// EncodeIdentity produces a versioned, opaque shell-safe token. NUL-delimited
// fields preserve path bytes, including newlines, without JSON's UTF-8 coercion.
func EncodeIdentity(identity Identity) (string, error) {
	if err := identity.validate(); err != nil {
		return "", err
	}
	if len(identity.ID)+len(identity.Key)+len(identity.Path)+2 > (maxIdentityTokenSize-len(identityPrefix))*3/4 {
		return "", errors.New("previous workspace identity is too large")
	}
	return identityPrefix + base64.RawURLEncoding.EncodeToString([]byte(identity.ID+"\x00"+identity.Key+"\x00"+identity.Path)), nil
}

func DecodeIdentity(token string) (Identity, error) {
	var identity Identity
	if !strings.HasPrefix(token, identityPrefix) || len(token) > maxIdentityTokenSize {
		return identity, errors.New("invalid or unsupported previous workspace token")
	}
	encoded := strings.TrimPrefix(token, identityPrefix)
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return identity, errors.New("malformed previous workspace token")
	}
	fields := strings.Split(string(decoded), "\x00")
	if len(fields) != 3 {
		return identity, errors.New("malformed previous workspace identity")
	}
	identity = Identity{ID: fields[0], Key: fields[1], Path: fields[2]}
	if err := identity.validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

// ErrNotInitialized identifies a direct executable invocation without a wrapper.
var ErrNotInitialized = errors.New("current-shell integration is not initialized")

// SetupError explains an unavailable or unsafe handoff before callers mutate
// application state. Errors.Is/As can still inspect the underlying error.
type SetupError struct{ Err error }

func (e *SetupError) Error() string {
	return fmt.Sprintf("shell integration: %v; initialize with eval \"$(workdrive config shell init zsh)\" (use bash for Bash), then invoke workdrive through the shell function", e.Err)
}
func (e *SetupError) Unwrap() error { return e.Err }

// Invocation owns a capability captured from the environment. It is intended
// for one CLI invocation, not concurrent use. Close it when that invocation ends.
type Invocation struct {
	directive string
	previous  string
	file      *os.File
	closed    bool
}

// Capture removes both environment values immediately, including on failure,
// so application children, adapters, hooks and nested shells cannot inherit them.
func Capture() (*Invocation, error) {
	invocation := NewInvocation(os.Getenv(DirectiveEnv), os.Getenv(PreviousEnv))
	err := errors.Join(os.Unsetenv(DirectiveEnv), os.Unsetenv(PreviousEnv))
	return invocation, err
}

// NewInvocation injects capabilities without reading or changing the environment.
func NewInvocation(directive, previous string) *Invocation {
	return &Invocation{directive: directive, previous: previous}
}

func (i *Invocation) PreviousToken() string {
	if i == nil {
		return ""
	}
	return i.previous
}

// Preflight opens an existing private file without following its final symlink.
// O_NONBLOCK prevents a malicious FIFO from hanging validation. The validated
// handle is retained: Publish never reopens a pathname that could be replaced.
func (i *Invocation) Preflight() error {
	fail := func(err error) error { return &SetupError{Err: err} }
	if i == nil || i.directive == "" {
		return fail(ErrNotInitialized)
	}
	if i.closed {
		return fail(os.ErrClosed)
	}
	if i.file != nil {
		return nil
	}
	if !filepath.IsAbs(i.directive) {
		return fail(errors.New("handoff file path must be absolute"))
	}
	// Validate the immediate private directory by handle as well. Ancestor
	// symlinks such as macOS /tmp are legitimate, but neither the private
	// directory nor the file itself may be a symlink.
	parent, name := filepath.Split(i.directive)
	if name == "" {
		return fail(errors.New("handoff path must name a file, not a directory"))
	}
	parent = strings.TrimRight(parent, string(os.PathSeparator))
	if parent == "" {
		parent = string(os.PathSeparator)
	}
	// Split rather than Dir/Clean preserves the kernel's traversal of symlink
	// ancestors followed by '..', matching the pathname the wrapper will read.
	directory, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(fmt.Errorf("open private handoff directory: %w", err))
	}
	defer unix.Close(directory)
	var directoryStat unix.Stat_t
	if err := unix.Fstat(directory, &directoryStat); err != nil {
		return fail(err)
	}
	if directoryStat.Uid != uint32(os.Geteuid()) || directoryStat.Mode&07777 != 0700 {
		return fail(errors.New("handoff directory must be private and owner-held (mode 0700)"))
	}
	fd, err := unix.Openat(directory, name, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(fmt.Errorf("open handoff file: %w", err))
	}
	file := os.NewFile(uintptr(fd), i.directive)
	info, err := file.Stat()
	if err == nil {
		var stat unix.Stat_t
		err = unix.Fstat(fd, &stat)
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Size() != 0) {
			err = errors.New("handoff file must be empty, private and owner-held (regular file, mode 0600, one link)")
		}
	}
	if err != nil {
		_ = file.Close()
		return fail(err)
	}
	i.file = file
	return nil
}

// Publish writes exactly four NUL-terminated fields: version, absolute literal
// destination, history action, and token. A nil previous keeps shell history;
// a nonnil previous replaces it only after the wrapper's cd succeeds.
func (i *Invocation) Publish(destination string, previous *Identity) error {
	if !filepath.IsAbs(destination) || strings.ContainsRune(destination, 0) {
		return errors.New("shell destination must be absolute and contain no NUL")
	}
	action, token := "keep", ""
	if previous != nil {
		var err error
		token, err = EncodeIdentity(*previous)
		if err != nil {
			return err
		}
		action = "replace"
	}
	if err := i.Preflight(); err != nil {
		return err
	}
	record := strings.Join([]string{protocolVersion, destination, action, token, ""}, "\x00")
	if err := i.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate shell handoff: %w", err)
	}
	if _, err := i.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek shell handoff: %w", err)
	}
	if _, err := io.WriteString(i.file, record); err != nil {
		return fmt.Errorf("write shell handoff: %w", err)
	}
	if err := i.file.Sync(); err != nil {
		return fmt.Errorf("flush shell handoff: %w", err)
	}
	return nil
}

func (i *Invocation) Close() error {
	if i == nil || i.closed {
		return nil
	}
	i.closed = true
	if i.file == nil {
		return nil
	}
	err := i.file.Close()
	i.file = nil
	return err
}
