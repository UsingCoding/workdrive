// Command terminal runs a process in the container with either captured pipes or a PTY.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

type step struct {
	Wait string `json:"wait"`
	Send string `json:"send"`
}

type request struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Dir     string            `json:"dir"`
	Steps   []step            `json:"steps"`
	Write   string            `json:"write"`
	Data    string            `json:"data"`
	Remove  string            `json:"remove"`
}

type response struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

var escapes = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

func main() {
	result := response{ExitCode: -1}
	if len(os.Args) != 2 {
		result.Error = "expected one base64 JSON request"
	} else if data, err := base64.StdEncoding.DecodeString(os.Args[1]); err != nil {
		result.Error = err.Error()
	} else {
		var req request
		if err := json.Unmarshal(data, &req); err != nil {
			result.Error = err.Error()
		} else {
			result = run(req)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(req request) response {
	if req.Write != "" {
		data, err := base64.StdEncoding.DecodeString(req.Data)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(req.Write), 0700)
		}
		if err == nil {
			err = os.WriteFile(req.Write, data, 0600)
		}
		return finished(err)
	}
	if req.Remove != "" {
		return finished(os.Remove(req.Remove))
	}
	if req.Command == "" {
		return response{ExitCode: -1, Error: "missing command"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, req.Command, req.Args...)
	cmd.Dir = req.Dir
	cmd.Env = os.Environ()
	for key, value := range req.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if len(req.Steps) > 0 {
		return interactive(ctx, cmd, req.Steps)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := finished(err)
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	return result
}

func finished(err error) response {
	if err == nil {
		return response{ExitCode: 0}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return response{ExitCode: exit.ExitCode()}
	}
	return response{ExitCode: -1, Error: err.Error()}
}

type transcript struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	update chan struct{}
}

func (t *transcript) read(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.buffer.Write(buf[:n])
			t.mu.Unlock()
			select {
			case t.update <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

func (t *transcript) snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buffer.String()
}

func interactive(ctx context.Context, cmd *exec.Cmd, steps []step) response {
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return finished(err)
	}
	defer terminal.Close()
	output := &transcript{update: make(chan struct{}, 1)}
	go output.read(terminal)
	cursor := 0
	for _, action := range steps {
		deadline := time.NewTimer(12 * time.Second)
		for {
			raw := output.snapshot()
			if strings.Contains(escapes.ReplaceAllString(raw[cursor:], ""), action.Wait) {
				cursor = len(raw)
				break
			}
			select {
			case <-output.update:
			case <-deadline.C:
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return response{ExitCode: -1, Output: output.snapshot(), Error: "timed out waiting for " + action.Wait}
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return response{ExitCode: -1, Output: output.snapshot(), Error: ctx.Err().Error()}
			}
		}
		deadline.Stop()
		if _, err := io.WriteString(terminal, action.Send); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return response{ExitCode: -1, Output: output.snapshot(), Error: err.Error()}
		}
	}
	err = cmd.Wait()
	return response{ExitCode: finished(err).ExitCode, Output: output.snapshot(), Error: finished(err).Error}
}
