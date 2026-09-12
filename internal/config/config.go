// Package config loads dispatch's layered configuration.
//
// Resolution order: built-in defaults, then ~/.config/dispatch/config.yaml,
// then an optional <repo>/.dispatch/config.yaml. Later layers override earlier
// ones field by field, so a project only states what it wants to change.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/matthewalunni/dispatch/internal/paths"
)

// RuntimeConfig maps a dispatch runtime name onto a herdr agent kind.
//
// This is why adding Codex or another terminal agent is a config change and
// not a Go change: `runtimes: {codex: {kind: codex}}` is enough.
type RuntimeConfig struct {
	// Kind is the herdr `--kind` label (claude, codex, gemini, ...).
	Kind string `yaml:"kind" json:"kind"`
	// Binary is the executable dispatch checks for in `dispatch doctor`.
	Binary string `yaml:"binary" json:"binary"`
	// Args are appended after `--` when starting the agent.
	Args []string `yaml:"args" json:"args,omitempty"`
	// PromptOnLaunch sends the initial assignment through herdr after the
	// agent is interactive. Disable for runtimes that take a prompt argument.
	PromptOnLaunch *bool `yaml:"prompt_on_launch" json:"prompt_on_launch,omitempty"`
}

// HerdrConfig describes how to reach the herdr runtime.
type HerdrConfig struct {
	Binary string `yaml:"binary" json:"binary"`
	// Session selects a named herdr session (`herdr --session <name>`).
	Session string `yaml:"session" json:"session,omitempty"`
	// Workspace pins dispatch tabs to one workspace id (e.g. "w1"). Empty
	// means "use the focused workspace".
	Workspace string `yaml:"workspace" json:"workspace,omitempty"`
	// StartTimeoutMS is how long herdr waits for the agent to become
	// interactive before reporting agent_not_ready.
	StartTimeoutMS int `yaml:"start_timeout_ms" json:"start_timeout_ms"`
	// FocusOnCreate brings the new tab to the front when a task is dispatched.
	FocusOnCreate bool `yaml:"focus_on_create" json:"focus_on_create"`
}

// Config is the merged dispatch configuration.
type Config struct {
	DefaultRole    string                   `yaml:"default_role" json:"default_role"`
	DefaultRuntime string                   `yaml:"default_runtime" json:"default_runtime"`
	WorktreesDir   string                   `yaml:"worktrees_dir" json:"worktrees_dir"`
	BranchPrefix   string                   `yaml:"branch_prefix" json:"branch_prefix"`
	Herdr          HerdrConfig              `yaml:"herdr" json:"herdr"`
	Runtimes       map[string]RuntimeConfig `yaml:"runtimes" json:"runtimes"`

	// ProjectInstructions is only meaningful in a project config. It is added
	// verbatim to every initial prompt dispatched from that repository.
	ProjectInstructions string `yaml:"project_instructions" json:"project_instructions,omitempty"`

	// Sources records which files contributed, for `dispatch doctor`.
	Sources []string `yaml:"-" json:"sources,omitempty"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		DefaultRole:    "general",
		DefaultRuntime: "claude",
		WorktreesDir:   paths.DefaultWorktreesDir(),
		BranchPrefix:   "dispatch/",
		Herdr: HerdrConfig{
			Binary:         "herdr",
			StartTimeoutMS: 60000,
			FocusOnCreate:  false,
		},
		Runtimes: map[string]RuntimeConfig{
			"claude": {Kind: "claude", Binary: "claude"},
			"codex":  {Kind: "codex", Binary: "codex"},
		},
	}
}

// DefaultFileContents is the commented config.yaml written on first run.
const DefaultFileContents = `# dispatch configuration
#
# Every value here can be overridden per repository by committing a
# .dispatch/config.yaml file; project values win field by field.

# Role used when --role is not given.
default_role: general

# Runtime used when a role does not name one.
default_runtime: claude

# Where isolated git worktrees are created. Never inside your source repo.
worktrees_dir: ~/.local/share/dispatch/worktrees

# Prefix for branches dispatch creates for worktree-isolated tasks.
branch_prefix: dispatch/

herdr:
  binary: herdr
  # session: work          # use a named herdr session
  # workspace: w1          # pin dispatch tabs to one workspace
  start_timeout_ms: 60000
  focus_on_create: false

# Runtimes map a dispatch runtime name onto a herdr agent kind. Adding a new
# terminal agent is a config change, not a code change.
runtimes:
  claude:
    kind: claude
    binary: claude
    # args: ["--model", "opus"]
  codex:
    kind: codex
    binary: codex
`

// Load reads the global config, layering it over the defaults.
func Load(configFile string) (Config, error) {
	cfg := Defaults()
	layer, found, err := readFile(configFile)
	if err != nil {
		return cfg, err
	}
	if found {
		cfg = merge(cfg, layer)
		cfg.Sources = append(cfg.Sources, configFile)
	}
	cfg.normalize()
	return cfg, nil
}

// ApplyProject layers a repository's optional .dispatch/config.yaml on top.
func (c Config) ApplyProject(projectConfigFile string) (Config, error) {
	layer, found, err := readFile(projectConfigFile)
	if err != nil {
		return c, err
	}
	if !found {
		return c, nil
	}
	out := merge(c, layer)
	out.Sources = append(append([]string(nil), c.Sources...), projectConfigFile)
	out.normalize()
	return out, nil
}

// Seed writes the default config file if none exists. It never overwrites.
func Seed(configFile string) (bool, error) {
	if _, err := os.Stat(configFile); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(configFile), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(configFile, []byte(DefaultFileContents), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", configFile, err)
	}
	return true, nil
}

// Runtime looks up a runtime definition by name.
func (c Config) Runtime(name string) (RuntimeConfig, error) {
	rt, ok := c.Runtimes[name]
	if !ok {
		return RuntimeConfig{}, fmt.Errorf("unknown runtime %q (configured: %s)", name, joinKeys(c.Runtimes))
	}
	if rt.Kind == "" {
		rt.Kind = name
	}
	if rt.Binary == "" {
		rt.Binary = rt.Kind
	}
	return rt, nil
}

// RuntimeNames lists configured runtimes, sorted.
func (c Config) RuntimeNames() []string {
	out := make([]string, 0, len(c.Runtimes))
	for name := range c.Runtimes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func joinKeys(m map[string]RuntimeConfig) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}

func (c *Config) normalize() {
	c.WorktreesDir = paths.Expand(c.WorktreesDir)
	if c.WorktreesDir == "" {
		c.WorktreesDir = paths.DefaultWorktreesDir()
	}
	if c.BranchPrefix == "" {
		c.BranchPrefix = "dispatch/"
	}
	if c.Herdr.Binary == "" {
		c.Herdr.Binary = "herdr"
	}
	if c.Herdr.StartTimeoutMS <= 0 {
		c.Herdr.StartTimeoutMS = 60000
	}
	if c.DefaultRole == "" {
		c.DefaultRole = "general"
	}
	if c.DefaultRuntime == "" {
		c.DefaultRuntime = "claude"
	}
	if c.Runtimes == nil {
		c.Runtimes = map[string]RuntimeConfig{}
	}
}

// fileLayer mirrors Config with pointers so "absent" and "zero" differ.
type fileLayer struct {
	DefaultRole         *string                  `yaml:"default_role"`
	DefaultRuntime      *string                  `yaml:"default_runtime"`
	WorktreesDir        *string                  `yaml:"worktrees_dir"`
	BranchPrefix        *string                  `yaml:"branch_prefix"`
	ProjectInstructions *string                  `yaml:"project_instructions"`
	Herdr               *herdrLayer              `yaml:"herdr"`
	Runtimes            map[string]RuntimeConfig `yaml:"runtimes"`
}

type herdrLayer struct {
	Binary         *string `yaml:"binary"`
	Session        *string `yaml:"session"`
	Workspace      *string `yaml:"workspace"`
	StartTimeoutMS *int    `yaml:"start_timeout_ms"`
	FocusOnCreate  *bool   `yaml:"focus_on_create"`
}

func readFile(path string) (fileLayer, bool, error) {
	var layer fileLayer
	if path == "" {
		return layer, false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return layer, false, nil
	}
	if err != nil {
		return layer, false, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &layer); err != nil {
		return layer, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	return layer, true, nil
}

func merge(base Config, layer fileLayer) Config {
	out := base
	if layer.DefaultRole != nil {
		out.DefaultRole = *layer.DefaultRole
	}
	if layer.DefaultRuntime != nil {
		out.DefaultRuntime = *layer.DefaultRuntime
	}
	if layer.WorktreesDir != nil {
		out.WorktreesDir = *layer.WorktreesDir
	}
	if layer.BranchPrefix != nil {
		out.BranchPrefix = *layer.BranchPrefix
	}
	if layer.ProjectInstructions != nil {
		out.ProjectInstructions = *layer.ProjectInstructions
	}
	if layer.Herdr != nil {
		if layer.Herdr.Binary != nil {
			out.Herdr.Binary = *layer.Herdr.Binary
		}
		if layer.Herdr.Session != nil {
			out.Herdr.Session = *layer.Herdr.Session
		}
		if layer.Herdr.Workspace != nil {
			out.Herdr.Workspace = *layer.Herdr.Workspace
		}
		if layer.Herdr.StartTimeoutMS != nil {
			out.Herdr.StartTimeoutMS = *layer.Herdr.StartTimeoutMS
		}
		if layer.Herdr.FocusOnCreate != nil {
			out.Herdr.FocusOnCreate = *layer.Herdr.FocusOnCreate
		}
	}
	// Runtimes augment rather than replace: a project adding one runtime
	// keeps every globally configured runtime available.
	if len(layer.Runtimes) > 0 {
		merged := make(map[string]RuntimeConfig, len(out.Runtimes)+len(layer.Runtimes))
		for k, v := range out.Runtimes {
			merged[k] = v
		}
		for k, v := range layer.Runtimes {
			existing := merged[k]
			if v.Kind != "" {
				existing.Kind = v.Kind
			}
			if v.Binary != "" {
				existing.Binary = v.Binary
			}
			if len(v.Args) > 0 {
				existing.Args = v.Args
			}
			if v.PromptOnLaunch != nil {
				existing.PromptOnLaunch = v.PromptOnLaunch
			}
			merged[k] = existing
		}
		out.Runtimes = merged
	}
	return out
}
