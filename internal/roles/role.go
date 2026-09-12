package roles

import (
	"fmt"
	"sort"
	"strings"
)

// Context declares what the launched agent should be told to look at. It is
// intentionally about *discovery*, not about copying files into the prompt.
type Context struct {
	// Repository tells the agent it is operating inside an existing project.
	Repository bool `yaml:"repository" json:"repository"`
	// DiscoverProjectDocs asks the agent to locate repository-specific agent
	// instructions and documentation before starting.
	DiscoverProjectDocs bool `yaml:"discover_project_docs" json:"discover_project_docs"`
	// GitHistory asks the agent to use git history as evidence.
	GitHistory bool `yaml:"git_history" json:"git_history"`
	// ExtraDiscovery adds role-specific things worth inspecting, e.g. a
	// designer role listing "design tokens" or "existing UI components".
	// Declarative so new specialisms never need Go changes.
	ExtraDiscovery []string `yaml:"extra_discovery" json:"extra_discovery,omitempty"`
}

// Role is a declarative agent specialism loaded from YAML.
type Role struct {
	Name         string        `yaml:"name" json:"name"`
	Description  string        `yaml:"description" json:"description"`
	Runtime      string        `yaml:"runtime" json:"runtime"`
	Isolation    IsolationMode `yaml:"isolation" json:"isolation"`
	Context      Context       `yaml:"context" json:"context"`
	Instructions string        `yaml:"instructions" json:"instructions"`

	// RuntimeArgs are passed through to the underlying agent binary.
	RuntimeArgs []string `yaml:"runtime_args" json:"runtime_args,omitempty"`
	// AgentPrefix overrides the herdr agent-name prefix for this role.
	AgentPrefix string `yaml:"agent_prefix" json:"agent_prefix,omitempty"`
	// InstructionsAppend lets a project-local role add to the global role's
	// instructions instead of restating them.
	InstructionsAppend string `yaml:"instructions_append" json:"instructions_append,omitempty"`

	// Source records where the definition was loaded from (for `roles show`).
	Source string `yaml:"-" json:"source,omitempty"`
	// Origin is "global", "project" or "merged".
	Origin string `yaml:"-" json:"origin,omitempty"`
}

// Validate checks a role after loading and merging.
func (r *Role) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("role is missing a name")
	}
	if !validRoleName(r.Name) {
		return fmt.Errorf("role name %q must be lowercase letters, digits, '-' or '_'", r.Name)
	}
	if strings.TrimSpace(r.Runtime) == "" {
		return fmt.Errorf("role %q is missing a runtime", r.Name)
	}
	if r.Isolation == "" {
		return fmt.Errorf("role %q is missing an isolation mode (%s)", r.Name, JoinIsolationModes())
	}
	if _, err := ParseIsolation(string(r.Isolation)); err != nil {
		return fmt.Errorf("role %q: %w", r.Name, err)
	}
	if strings.TrimSpace(r.Instructions) == "" {
		return fmt.Errorf("role %q has no instructions", r.Name)
	}
	return nil
}

func validRoleName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return name != ""
}

// MergeOver layers the receiver (a project override) on top of base.
//
// Any field the override leaves empty keeps the base value, so a project role
// file can be three lines long and still inherit a full global definition.
func (r Role) MergeOver(base Role) Role {
	out := base
	out.Origin = "merged"
	if r.Name != "" {
		out.Name = r.Name
	}
	if r.Description != "" {
		out.Description = r.Description
	}
	if r.Runtime != "" {
		out.Runtime = r.Runtime
	}
	if r.Isolation != "" {
		out.Isolation = r.Isolation
	}
	if r.Instructions != "" {
		out.Instructions = r.Instructions
	}
	if r.InstructionsAppend != "" {
		out.Instructions = strings.TrimRight(out.Instructions, "\n") + "\n" + strings.TrimSpace(r.InstructionsAppend) + "\n"
	}
	if len(r.RuntimeArgs) > 0 {
		out.RuntimeArgs = append([]string(nil), r.RuntimeArgs...)
	}
	if r.AgentPrefix != "" {
		out.AgentPrefix = r.AgentPrefix
	}
	// Context booleans are only ever turned on by an override; a project that
	// wants a context flag off should redefine the role wholesale.
	if r.Context.Repository {
		out.Context.Repository = true
	}
	if r.Context.DiscoverProjectDocs {
		out.Context.DiscoverProjectDocs = true
	}
	if r.Context.GitHistory {
		out.Context.GitHistory = true
	}
	if len(r.Context.ExtraDiscovery) > 0 {
		out.Context.ExtraDiscovery = dedupe(append(append([]string(nil), out.Context.ExtraDiscovery...), r.Context.ExtraDiscovery...))
	}
	if r.Source != "" {
		out.Source = r.Source
	}
	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, v := range in {
		key := strings.ToLower(strings.TrimSpace(v))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}

// SortRoles orders roles by name for stable output.
func SortRoles(in []Role) {
	sort.Slice(in, func(i, j int) bool { return in[i].Name < in[j].Name })
}
