package roles

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml builtin/README.md
var builtinFS embed.FS

// ErrNotFound is returned when no role matches a name.
var ErrNotFound = errors.New("role not found")

// UnknownRoleError carries the available names so the CLI can be helpful.
type UnknownRoleError struct {
	Name      string
	Available []string
}

func (e *UnknownRoleError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("unknown role %q and no roles are installed", e.Name)
	}
	return fmt.Sprintf("unknown role %q (available: %s)", e.Name, strings.Join(e.Available, ", "))
}

func (e *UnknownRoleError) Unwrap() error { return ErrNotFound }

// Seed writes the default role files and README into dir without ever
// clobbering a file the user already has. Safe to call on every run.
func Seed(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create roles directory %s: %w", dir, err)
	}
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, err
	}
	var written []string
	for _, entry := range entries {
		target := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(target); err == nil {
			continue // user owns this file now
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		data, err := builtinFS.ReadFile("builtin/" + entry.Name())
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", target, err)
		}
		written = append(written, target)
	}
	sort.Strings(written)
	return written, nil
}

// BuiltinNames lists the roles this build ships by default.
func BuiltinNames() []string {
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".yaml") {
			out = append(out, strings.TrimSuffix(entry.Name(), ".yaml"))
		}
	}
	sort.Strings(out)
	return out
}

// Template returns a starter role file for `dispatch role init`.
func Template(name string) string {
	return fmt.Sprintf(`name: %s
description: Describe what this specialist is for
runtime: claude
isolation: none
context:
  repository: true
  discover_project_docs: true
  extra_discovery:
    - anything this specialist should always look at first
instructions: |
  Describe how this agent should work.

  Say what it owns, what evidence it should gather before acting, what it
  should not do, and what it should do when the project is ambiguous.
`, name)
}

// LoadDir reads every role file in dir. A missing directory is not an error.
func LoadDir(dir, origin string) ([]Role, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read roles directory %s: %w", dir, err)
	}
	var out []Role
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		role, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		if role.Name == "" {
			role.Name = strings.TrimSuffix(entry.Name(), ext)
		}
		role.Origin = origin
		out = append(out, role)
	}
	SortRoles(out)
	return out, nil
}

// LoadFile parses a single role YAML file.
func LoadFile(path string) (Role, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Role{}, fmt.Errorf("read role %s: %w", path, err)
	}
	var role Role
	if err := yaml.Unmarshal(data, &role); err != nil {
		return Role{}, fmt.Errorf("parse role %s: %w", path, err)
	}
	role.Source = path
	return role, nil
}

// Set is a resolved collection of roles: global definitions with any
// project-local definitions already layered on top.
type Set struct {
	byName map[string]Role
}

// Resolve merges global and project role directories.
func Resolve(globalDir, projectDir string) (*Set, error) {
	set := &Set{byName: map[string]Role{}}

	global, err := LoadDir(globalDir, "global")
	if err != nil {
		return nil, err
	}
	for _, role := range global {
		set.byName[role.Name] = role
	}

	if projectDir != "" {
		project, err := LoadDir(projectDir, "project")
		if err != nil {
			return nil, err
		}
		for _, role := range project {
			if base, ok := set.byName[role.Name]; ok {
				set.byName[role.Name] = role.MergeOver(base)
				continue
			}
			set.byName[role.Name] = role
		}
	}
	return set, nil
}

// Get returns a role by name.
func (s *Set) Get(name string) (Role, error) {
	role, ok := s.byName[name]
	if !ok {
		return Role{}, &UnknownRoleError{Name: name, Available: s.Names()}
	}
	if err := role.Validate(); err != nil {
		return Role{}, err
	}
	return role, nil
}

// Names lists every resolved role name, sorted.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.byName))
	for name := range s.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// All returns every resolved role, sorted by name.
func (s *Set) All() []Role {
	out := make([]Role, 0, len(s.byName))
	for _, role := range s.byName {
		out = append(out, role)
	}
	SortRoles(out)
	return out
}

// Len reports how many roles resolved.
func (s *Set) Len() int { return len(s.byName) }
