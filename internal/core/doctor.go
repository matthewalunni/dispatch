package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/matthewalunni/dispatch/internal/paths"
	"github.com/matthewalunni/dispatch/internal/runtime"
)

// CheckState is a diagnostic outcome.
type CheckState string

const (
	CheckOK       CheckState = "ok"
	CheckWarn     CheckState = "warn"
	CheckFail     CheckState = "fail"
	CheckAbsent   CheckState = "absent"
	CheckInfoOnly CheckState = "info"
)

// Check is one diagnostic line.
type Check struct {
	Label  string     `json:"label"`
	State  CheckState `json:"state"`
	Detail string     `json:"detail,omitempty"`
	// Fix is the actionable next step when something is wrong.
	Fix string `json:"fix,omitempty"`
}

// CheckGroup is a named section of the report.
type CheckGroup struct {
	Name   string  `json:"name"`
	Checks []Check `json:"checks"`
}

// Diagnosis is the full `dispatch doctor` report.
type Diagnosis struct {
	Version string       `json:"version"`
	Groups  []CheckGroup `json:"groups"`
	Healthy bool         `json:"healthy"`
}

// Doctor inspects the environment and reports what is and is not usable.
func (a *App) Doctor(ctx context.Context, dir, version string) (*Diagnosis, error) {
	d := &Diagnosis{Version: version, Healthy: true}

	rc, resolveErr := a.Resolve(dir)

	// Configuration -------------------------------------------------------
	configGroup := CheckGroup{Name: "Configuration"}
	configGroup.Checks = append(configGroup.Checks, fileCheck(a.configFile, "config.yaml is created on first run"))
	configGroup.Checks = append(configGroup.Checks, dirCheck(a.rolesDir, "default roles are seeded on first run"))

	roleCount := 0
	if rc != nil {
		roleCount = rc.Roles.Len()
	}
	roleCheck := Check{Label: "roles installed", State: CheckOK, Detail: fmt.Sprintf("%d", roleCount)}
	if roleCount == 0 {
		roleCheck.State = CheckFail
		roleCheck.Fix = "dispatch roles     # re-seeds the defaults"
	}
	configGroup.Checks = append(configGroup.Checks, roleCheck)
	configGroup.Checks = append(configGroup.Checks, dirCheck(paths.WorkflowsDir(), ""))
	d.Groups = append(d.Groups, configGroup)

	// State ---------------------------------------------------------------
	stateGroup := CheckGroup{Name: "State"}
	stateGroup.Checks = append(stateGroup.Checks, fileCheck(a.store.Path(), "created on first run"))
	if counts, err := a.store.Count(ctx); err == nil {
		stateGroup.Checks = append(stateGroup.Checks, Check{
			Label:  "tasks",
			State:  CheckInfoOnly,
			Detail: fmt.Sprintf("%d total, %d active", counts.Total, counts.Active),
		})
	} else {
		stateGroup.Checks = append(stateGroup.Checks, Check{Label: "tasks", State: CheckFail, Detail: err.Error()})
	}
	stateGroup.Checks = append(stateGroup.Checks, dirCheck(paths.SessionsDir(), ""))
	worktreesDir := paths.DefaultWorktreesDir()
	if rc != nil {
		worktreesDir = rc.Config.WorktreesDir
	}
	stateGroup.Checks = append(stateGroup.Checks, Check{
		Label: "worktrees", State: CheckInfoOnly, Detail: worktreesDir,
	})
	d.Groups = append(d.Groups, stateGroup)

	// Herdr ---------------------------------------------------------------
	health := a.herdr.Health(ctx)
	herdrGroup := CheckGroup{Name: "Herdr"}
	installed := Check{Label: "installed", State: CheckFail}
	if health.Installed {
		installed.State = CheckOK
		installed.Detail = health.BinaryPath
		if health.ClientVersion != "" {
			installed.Detail += " (" + health.ClientVersion + ")"
		}
	} else {
		installed.Fix = "curl -fsSL https://herdr.dev/install.sh | sh"
	}
	herdrGroup.Checks = append(herdrGroup.Checks, installed)

	reachable := Check{Label: "server reachable", State: CheckFail}
	switch {
	case !health.Installed:
		reachable.Detail = "herdr not installed"
	case health.ServerRunning:
		reachable.State = CheckOK
		reachable.Detail = health.Socket
		if health.ServerVersion != "" {
			reachable.Detail = health.ServerVersion + " at " + health.Socket
		}
	default:
		reachable.Detail = health.Detail
		reachable.Fix = "herdr     # start or attach a herdr session"
	}
	herdrGroup.Checks = append(herdrGroup.Checks, reachable)

	if health.ServerRunning {
		compatible := Check{Label: "protocol compatible", State: CheckOK}
		if !health.Compatible {
			compatible.State = CheckWarn
			compatible.Detail = health.Detail
			compatible.Fix = "herdr server stop && herdr"
		}
		herdrGroup.Checks = append(herdrGroup.Checks, compatible)

		if workspaces, err := a.herdr.Workspaces(ctx); err == nil {
			herdrGroup.Checks = append(herdrGroup.Checks, Check{
				Label: "workspaces", State: CheckInfoOnly, Detail: fmt.Sprintf("%d", len(workspaces)),
			})
		}
		if agents, err := a.herdr.ListAgents(ctx); err == nil {
			herdrGroup.Checks = append(herdrGroup.Checks, Check{
				Label: "live agents", State: CheckInfoOnly, Detail: fmt.Sprintf("%d", len(agents)),
			})
		}
	}
	d.Groups = append(d.Groups, herdrGroup)

	// Runtimes ------------------------------------------------------------
	runtimeGroup := CheckGroup{Name: "Runtimes"}
	if rc != nil {
		for _, name := range rc.Config.RuntimeNames() {
			rt, err := runtime.Resolve(rc.Config, name)
			if err != nil {
				runtimeGroup.Checks = append(runtimeGroup.Checks, Check{Label: name, State: CheckFail, Detail: err.Error()})
				continue
			}
			check := Check{Label: name}
			if path, err := runtime.Installed(rt); err == nil {
				check.State = CheckOK
				check.Detail = path
			} else {
				check.State = CheckWarn
				check.Detail = fmt.Sprintf("%s not on PATH", rt.Binary())
				check.Fix = fmt.Sprintf("install %s, or remove runtimes.%s from %s", rt.Binary(), name, a.configFile)
			}
			runtimeGroup.Checks = append(runtimeGroup.Checks, check)
		}
	}
	d.Groups = append(d.Groups, runtimeGroup)

	// Git -----------------------------------------------------------------
	gitGroup := CheckGroup{Name: "Git"}
	gitCheck := Check{Label: "installed", State: CheckFail}
	if version, err := a.git.Available(); err == nil {
		gitCheck.State = CheckOK
		gitCheck.Detail = version
	} else {
		gitCheck.Detail = err.Error()
		gitCheck.Fix = "install git; worktree isolation needs it"
	}
	gitGroup.Checks = append(gitGroup.Checks, gitCheck)
	d.Groups = append(d.Groups, gitGroup)

	// Current project -----------------------------------------------------
	projectGroup := CheckGroup{Name: "Current project"}
	if resolveErr != nil {
		projectGroup.Checks = append(projectGroup.Checks, Check{Label: "detection", State: CheckFail, Detail: resolveErr.Error()})
	} else {
		proj := rc.Project
		projectGroup.Checks = append(projectGroup.Checks, Check{Label: proj.Root, State: CheckInfoOnly})

		repo := Check{Label: "git repository", State: CheckOK, Detail: "branch " + proj.Branch}
		switch {
		case !proj.IsGit:
			repo.State = CheckWarn
			repo.Detail = "not a git repository; worktree isolation is unavailable here"
			repo.Fix = "git init, or use --isolation none"
		case proj.DetachedHEAD:
			repo.State = CheckWarn
			repo.Detail = "detached HEAD; new branches will be cut from the current commit"
			repo.Fix = "git switch <branch>"
		case proj.Dirty:
			repo.Detail = fmt.Sprintf("branch %s (uncommitted changes)", proj.Branch)
		}
		projectGroup.Checks = append(projectGroup.Checks, repo)

		overrides := Check{Label: ".dispatch/", State: CheckAbsent, Detail: "not present"}
		if proj.HasDispatchDir {
			overrides.State = CheckOK
			overrides.Detail = proj.DispatchDir()
		}
		projectGroup.Checks = append(projectGroup.Checks, overrides)
		d.Groups = append(d.Groups, projectGroup)

		// Instructions discovered --------------------------------------
		discovered := CheckGroup{Name: "Context discoverable by agents"}
		for _, source := range proj.Sources {
			state := CheckAbsent
			if source.Present {
				state = CheckOK
			}
			discovered.Checks = append(discovered.Checks, Check{Label: source.Label, State: state})
		}
		d.Groups = append(d.Groups, discovered)
	}

	for _, group := range d.Groups {
		for _, check := range group.Checks {
			if check.State == CheckFail {
				d.Healthy = false
			}
		}
	}
	return d, nil
}

func fileCheck(path, fix string) Check {
	check := Check{Label: path}
	info, err := os.Stat(path)
	switch {
	case err == nil && !info.IsDir():
		check.State = CheckOK
	case errors.Is(err, fs.ErrNotExist):
		check.State = CheckFail
		check.Detail = "missing"
		check.Fix = fix
	default:
		check.State = CheckFail
		check.Detail = errText(err)
	}
	return check
}

func dirCheck(path, fix string) Check {
	check := Check{Label: path}
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		check.State = CheckOK
	case errors.Is(err, fs.ErrNotExist):
		check.State = CheckWarn
		check.Detail = "missing"
		check.Fix = fix
	default:
		check.State = CheckFail
		check.Detail = errText(err)
	}
	return check
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
