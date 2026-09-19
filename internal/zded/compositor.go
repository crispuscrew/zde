package zded

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/manifest"
)

// Compositor supplies live session state. FocusedPlace and Windows each return
// a consistent snapshot; Windows returns a caller-owned slice.
type Compositor interface {
	DeskMap() (*desk.Map, error)
	FocusedName() (string, error)
	FocusWorkspace(name string) error
	FocusedOutput() (string, error)
	FocusedPlace() (name, output string, err error)
	FocusedWindow() (uint64, error)
	MoveWindowToWorkspace(name string) error
	FocusWindowVertically(down bool) error
	Windows() ([]Window, error)
	FocusWindow(id uint64) error
	RenameWorkspace(from, to string) error
	SetWorkspaceNameByID(id uint64, name string) error
	FirstApps() (map[uint64]string, error)
	EmptyByOutput() (map[string][]uint64, error)
	Perform(action string) error
	ReloadConfig() error
}

// Desks loads current manifests on each call. All reports per-file problems
// without discarding readable desks; only a directory failure is an error.
type Desks interface {
	All() (map[string]*manifest.Desk, []manifest.Problem, error)
	Save(*manifest.Desk) (string, error)
}

type noDesks struct{}

func (noDesks) All() (map[string]*manifest.Desk, []manifest.Problem, error) { return nil, nil, nil }

func (noDesks) Save(*manifest.Desk) (string, error) {
	return "", fmt.Errorf("no desks directory to write to")
}
