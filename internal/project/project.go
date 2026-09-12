// Package project detects what repository or directory dispatch is operating
// in, and what context sources an agent could discover there.
//
// Discovery here is deliberately shallow: dispatch reports *which* context
// sources exist so the CLI can show them and the prompt can name them. It
// never reads their contents into the prompt — the launched agent does that
// itself, which is what keeps dispatch portable across arbitrary projects.
package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/matthewalunni/dispatch/internal/gitx"
	"github.com/matthewalunni/dispatch/internal/paths"
)

// Source is a context source an agent may want to inspect.
type Source struct {
	Label   string `json:"label"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Kind    string `json:"kind"` // instructions | docs | design | manifest | tests
}

// Project describes the working context of a dispatch invocation.
type Project struct {
	// Root is the repository root, or the working directory when not in a repo.
	Root string `json:"root"`
	// Name is the human-facing project name (the root directory name).
	Name string `json:"name"`
	// IsGit reports whether Root is a git working tree.
	IsGit bool `json:"is_git"`
	// Branch is the checked-out branch, empty when detached or not a repo.
	Branch string `json:"branch,omitempty"`
	// DetachedHEAD reports a detached checkout.
	DetachedHEAD bool `json:"detached_head"`
	// Dirty reports uncommitted changes in the working tree.
	Dirty bool `json:"dirty"`
	// HasDispatchDir reports an optional .dispatch/ override directory.
	HasDispatchDir bool `json:"has_dispatch_dir"`
	// Sources are the context sources detected in the project.
	Sources []Source `json:"sources"`
}

// ConfigFile is the project's optional config override path.
func (p Project) ConfigFile() string {
	if p.Root == "" {
		return ""
	}
	return filepath.Join(p.Root, paths.ProjectConfigDirName, "config.yaml")
}

// RolesDir is the project's optional role override directory.
func (p Project) RolesDir() string {
	if p.Root == "" {
		return ""
	}
	return filepath.Join(p.Root, paths.ProjectConfigDirName, "roles")
}

// DispatchDir is the project's optional override directory.
func (p Project) DispatchDir() string {
	if p.Root == "" {
		return ""
	}
	return filepath.Join(p.Root, paths.ProjectConfigDirName)
}

// PresentSources returns only the context sources that actually exist.
func (p Project) PresentSources() []Source {
	var out []Source
	for _, s := range p.Sources {
		if s.Present {
			out = append(out, s)
		}
	}
	return out
}

// candidates are the context sources dispatch knows how to look for. Keeping
// this list here — rather than in a role — is what lets any repository work
// without configuration.
var candidates = []struct {
	label string
	kind  string
	globs []string
}{
	{"CLAUDE.md", "instructions", []string{"CLAUDE.md"}},
	{"AGENTS.md", "instructions", []string{"AGENTS.md"}},
	{".claude/", "instructions", []string{".claude"}},
	{"README", "docs", []string{"README.md", "README.rst", "README.txt", "README"}},
	{"CONTRIBUTING", "docs", []string{"CONTRIBUTING.md"}},
	{"docs/", "docs", []string{"docs"}},
	{"design/", "design", []string{"design"}},
	{".design-system/", "design", []string{".design-system"}},
	{"package manifest", "manifest", []string{
		"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "requirements.txt",
		"Gemfile", "pom.xml", "build.gradle", "build.gradle.kts", "composer.json",
		"Package.swift", "pubspec.yaml", "mix.exs", "*.xcodeproj",
	}},
	{"Makefile/justfile", "manifest", []string{"Makefile", "justfile", "Justfile", "Taskfile.yml"}},
}

// Detect resolves the project for a working directory.
func Detect(git gitx.Git, dir string) (Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Project{}, err
	}

	p := Project{Root: abs}

	if root, err := git.RepoRoot(abs); err == nil && root != "" {
		p.Root = root
		p.IsGit = true
		if branch, err := git.CurrentBranch(root); err == nil {
			p.Branch = branch
		}
		p.DetachedHEAD = p.Branch == ""
		if dirty, err := git.IsDirty(root); err == nil {
			p.Dirty = dirty
		}
	}

	p.Name = filepath.Base(p.Root)
	if info, err := os.Stat(filepath.Join(p.Root, paths.ProjectConfigDirName)); err == nil && info.IsDir() {
		p.HasDispatchDir = true
	}
	p.Sources = detectSources(p.Root)
	return p, nil
}

func detectSources(root string) []Source {
	out := make([]Source, 0, len(candidates))
	for _, c := range candidates {
		src := Source{Label: c.label, Kind: c.kind}
		for _, pattern := range c.globs {
			if strings.ContainsAny(pattern, "*?[") {
				matches, err := filepath.Glob(filepath.Join(root, pattern))
				if err == nil && len(matches) > 0 {
					src.Present = true
					src.Path = matches[0]
					break
				}
				continue
			}
			path := filepath.Join(root, pattern)
			if _, err := os.Stat(path); err == nil {
				src.Present = true
				src.Path = path
				break
			}
		}
		out = append(out, src)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Present != out[j].Present {
			return out[i].Present
		}
		return false
	})
	return out
}
