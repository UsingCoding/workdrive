package app

import (
	"github.com/usingcoding/workdrive/internal/domain"
)

type RepositoryChoice struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	Branch string `json:"branch"`
	Base   string `json:"base,omitempty"`
	Path   string `json:"path"`
}

type NewRequest struct {
	Key     string             `json:"key"`
	Name    string             `json:"name"`
	Path    string             `json:"path,omitempty"`
	Profile string             `json:"profile"`
	Repos   []RepositoryChoice `json:"repos"`
}

type Plan struct {
	Kind      string             `json:"kind"`
	Workspace *domain.Workspace  `json:"workspace"`
	Remove    []domain.Resource  `json:"remove,omitempty"`
	Add       []RepositoryChoice `json:"add,omitempty"`
	Warnings  []string           `json:"warnings,omitempty"`
	Blockers  []string           `json:"blockers,omitempty"`
}

func (p Plan) Safe() bool { return len(p.Blockers) == 0 }
