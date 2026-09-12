package roles

// Field documents one role field for `dispatch roles schema`.
type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
	Doc      string `json:"doc"`
}

// Schema is the machine-readable description of the role file format.
type Schema struct {
	Version         int      `json:"version"`
	Fields          []Field  `json:"fields"`
	IsolationModes  []string `json:"isolation_modes"`
	ResolutionOrder []string `json:"resolution_order"`
}

// SchemaVersion is bumped when the role file format changes incompatibly.
const SchemaVersion = 1

// Describe returns the role schema for docs and machine consumers.
func Describe() Schema {
	modes := make([]string, 0, len(allIsolationModes))
	for _, mode := range allIsolationModes {
		modes = append(modes, string(mode))
	}
	return Schema{
		Version:        SchemaVersion,
		IsolationModes: modes,
		ResolutionOrder: []string{
			"global role (~/.config/dispatch/roles/<name>.yaml)",
			"project role (<repo>/.dispatch/roles/<name>.yaml)",
			"task options (--runtime, --isolation, --branch, ...)",
		},
		Fields: []Field{
			{Name: "name", Type: "string", Required: true, Doc: "Role identifier matched by --role. Lowercase letters, digits, '-' and '_'."},
			{Name: "description", Type: "string", Doc: "One-line summary shown by `dispatch roles`."},
			{Name: "runtime", Type: "string", Required: true, Default: "claude", Doc: "Agent runtime to launch. Must be defined in config.yaml `runtimes`."},
			{Name: "isolation", Type: "enum", Required: true, Default: "none", Doc: "Workspace dispatch prepares: none | worktree."},
			{Name: "context.repository", Type: "bool", Doc: "Tell the agent it is operating inside an existing project."},
			{Name: "context.discover_project_docs", Type: "bool", Doc: "Tell the agent to find repository-specific agent instructions and docs."},
			{Name: "context.git_history", Type: "bool", Doc: "Tell the agent to treat git history as evidence."},
			{Name: "context.extra_discovery", Type: "[]string", Doc: "Additional role-specific things the agent should inspect first."},
			{Name: "instructions", Type: "string", Required: true, Doc: "The role's standing orders, included verbatim in the initial prompt."},
			{Name: "runtime_args", Type: "[]string", Doc: "Extra arguments passed through to the agent binary."},
			{Name: "agent_prefix", Type: "string", Doc: "Prefix for the generated herdr agent name."},
			{Name: "instructions_append", Type: "string", Doc: "Project-local roles only: append to the global role's instructions instead of replacing them."},
		},
	}
}
