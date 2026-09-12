package clix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/matthewalunni/dispatch/internal/roles"
)

func newRolesCommand() *cobra.Command {
	var (
		asJSON bool
		dir    string
	)
	cmd := &cobra.Command{
		Use:   "roles",
		Short: "List the agent roles available here",
		Long: `List resolved roles: the global definitions in ~/.config/dispatch/roles with
any project-local overrides from .dispatch/roles layered on top.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			rc, err := app.Resolve(dir)
			if err != nil {
				return err
			}
			all := rc.Roles.All()
			if asJSON {
				return emitJSON(map[string]any{"roles": all})
			}
			if len(all) == 0 {
				fmt.Println(styleDim("no roles installed; run `dispatch doctor`"))
				return nil
			}
			tw := newTabWriter(os.Stdout)
			fmt.Fprintln(tw, styleDim("ROLE\tRUNTIME\tISOLATION\tSOURCE\tDESCRIPTION"))
			for _, role := range all {
				origin := role.Origin
				if len(role.Inherits) > 0 {
					origin += " ⊃ " + role.Inherits[0]
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					role.Name, role.Runtime, role.Isolation, origin, truncate(role.Description, 40))
			}
			tw.Flush()
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "resolve roles as if invoked from this directory")

	cmd.AddCommand(newRolesShowCommand(), newRolesSchemaCommand())
	return cmd
}

func newRolesShowCommand() *cobra.Command {
	var (
		asJSON bool
		dir    string
	)
	cmd := &cobra.Command{
		Use:   "show <role>",
		Short: "Show one role, with project overrides applied",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			rc, err := app.Resolve(dir)
			if err != nil {
				return err
			}
			role, err := rc.Roles.Get(args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(role)
			}

			fmt.Printf("%s  %s\n", styleAccent(role.Name), styleDim(role.Description))
			tw := newTabWriter(os.Stdout)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("runtime"), role.Runtime)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("isolation"), role.Isolation)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("source"), role.Source)
			fmt.Fprintf(tw, "%s\t%s\n", styleDim("origin"), role.Origin)
			if len(role.Inherits) > 0 {
				fmt.Fprintf(tw, "%s\t%s\n", styleDim("extends"), strings.Join(role.Inherits, " → "))
			}
			fmt.Fprintf(tw, "%s\t%t\n", styleDim("repository context"), role.Context.Repository)
			fmt.Fprintf(tw, "%s\t%t\n", styleDim("discover docs"), role.Context.DiscoverProjectDocs)
			fmt.Fprintf(tw, "%s\t%t\n", styleDim("git history"), role.Context.GitHistory)
			if len(role.RuntimeArgs) > 0 {
				fmt.Fprintf(tw, "%s\t%s\n", styleDim("runtime args"), strings.Join(role.RuntimeArgs, " "))
			}
			tw.Flush()

			if len(role.Context.ExtraDiscovery) > 0 {
				fmt.Printf("\n%s\n", styleDim("also inspects"))
				for _, item := range role.Context.ExtraDiscovery {
					fmt.Printf("  - %s\n", item)
				}
			}
			fmt.Printf("\n%s\n", styleDim("instructions"))
			for _, line := range strings.Split(strings.TrimRight(role.Instructions, "\n"), "\n") {
				fmt.Printf("  %s\n", line)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "resolve roles as if invoked from this directory")
	return cmd
}

func newRolesSchemaCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Describe the role file format",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			schema := roles.Describe()
			if asJSON {
				return emitJSON(schema)
			}
			fmt.Printf("%s  (version %d)\n\n", styleAccent("role file format"), schema.Version)
			tw := newTabWriter(os.Stdout)
			fmt.Fprintln(tw, styleDim("FIELD\tTYPE\tREQUIRED\tDESCRIPTION"))
			for _, field := range schema.Fields {
				required := ""
				if field.Required {
					required = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", field.Name, field.Type, required, field.Doc)
			}
			tw.Flush()
			fmt.Printf("\n%s %s\n", styleDim("isolation modes:"), strings.Join(schema.IsolationModes, ", "))
			fmt.Printf("\n%s\n", styleDim("resolution order"))
			for i, step := range schema.ResolutionOrder {
				fmt.Printf("  %d. %s\n", i+1, step)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable output")
	return cmd
}

func newRoleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "role",
		Short: "Create and manage role definitions",
	}
	cmd.AddCommand(newRoleInitCommand())
	return cmd
}

func newRoleInitCommand() *cobra.Command {
	var (
		projectLocal bool
		dir          string
	)
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Generate a starter role file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			app, err := openApp()
			if err != nil {
				return err
			}
			defer app.Close()

			target := app.RolesDir()
			if projectLocal {
				rc, err := app.Resolve(dir)
				if err != nil {
					return err
				}
				if rc.Project.RolesDir() == "" {
					return fmt.Errorf("cannot create a project role outside a project directory")
				}
				target = rc.Project.RolesDir()
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			path := filepath.Join(target, name+".yaml")
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists; edit it instead of re-initialising", path)
			}
			if err := os.WriteFile(path, []byte(roles.Template(name)), 0o644); err != nil {
				return err
			}
			fmt.Printf("%s %s\n", styleAccent("created"), path)
			fmt.Printf("edit it, then: dispatch roles show %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&projectLocal, "project", false, "create the role in this repository's .dispatch/roles instead")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "project directory for --project")
	return cmd
}
