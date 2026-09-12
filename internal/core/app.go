// Package core is dispatch's application layer.
//
// Everything the product can do lives here. The CLI and the Bubble Tea TUI are
// both thin callers of this package, which is what keeps external orchestrators
// from ever being coupled to the terminal UI.
package core

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/matthewalunni/dispatch/internal/config"
	"github.com/matthewalunni/dispatch/internal/gitx"
	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/paths"
	"github.com/matthewalunni/dispatch/internal/project"
	"github.com/matthewalunni/dispatch/internal/roles"
	"github.com/matthewalunni/dispatch/internal/store"
)

// Options configure an App. Every external dependency is injectable so the
// application layer can be exercised without a terminal, a git repo or herdr.
type Options struct {
	ConfigFile string
	RolesDir   string
	DBPath     string
	DataDir    string

	Git   gitx.Git
	Herdr herdrx.Client
	Now   func() time.Time

	// SkipSeed disables first-run seeding (used by tests).
	SkipSeed bool
}

// App is the dispatch application.
type App struct {
	cfg   config.Config
	store *store.Store
	git   gitx.Git
	herdr herdrx.Client
	now   func() time.Time

	configFile string
	rolesDir   string
	dataDir    string

	// seeded records what first-run setup created, for `dispatch doctor`.
	seeded []string
}

// New builds an App, performing safe first-run setup.
func New(opts Options) (*App, error) {
	if opts.ConfigFile == "" {
		opts.ConfigFile = paths.ConfigFile()
	}
	if opts.RolesDir == "" {
		opts.RolesDir = paths.RolesDir()
	}
	if opts.DBPath == "" {
		opts.DBPath = paths.DBPath()
	}
	if opts.DataDir == "" {
		opts.DataDir = paths.DataDir()
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Git == nil {
		opts.Git = gitx.New()
	}

	app := &App{
		git:        opts.Git,
		now:        opts.Now,
		configFile: opts.ConfigFile,
		rolesDir:   opts.RolesDir,
		dataDir:    opts.DataDir,
	}

	if !opts.SkipSeed {
		if err := app.seed(); err != nil {
			return nil, err
		}
	}

	cfg, err := config.Load(opts.ConfigFile)
	if err != nil {
		return nil, err
	}
	app.cfg = cfg

	if opts.Herdr != nil {
		app.herdr = opts.Herdr
	} else {
		app.herdr = herdrx.NewCLI(cfg.Herdr.Binary, cfg.Herdr.Session)
	}

	st, err := store.Open(opts.DBPath)
	if err != nil {
		return nil, err
	}
	app.store = st
	return app, nil
}

// seed creates the XDG directories, the default config and the default roles.
// It never overwrites anything the user has edited, so running dispatch
// repeatedly is always safe.
func (a *App) seed() error {
	if err := paths.EnsureBaseDirs(); err != nil {
		return fmt.Errorf("create dispatch directories: %w", err)
	}
	if err := os.MkdirAll(a.rolesDir, 0o755); err != nil {
		return err
	}
	wroteConfig, err := config.Seed(a.configFile)
	if err != nil {
		return err
	}
	if wroteConfig {
		a.seeded = append(a.seeded, a.configFile)
	}
	written, err := roles.Seed(a.rolesDir)
	if err != nil {
		return err
	}
	a.seeded = append(a.seeded, written...)
	return nil
}

// Close releases resources.
func (a *App) Close() error {
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}

// Config returns the global configuration.
func (a *App) Config() config.Config { return a.cfg }

// ConfigFile returns the global config path.
func (a *App) ConfigFile() string { return a.configFile }

// RolesDir returns the global roles directory.
func (a *App) RolesDir() string { return a.rolesDir }

// Store exposes the task registry.
func (a *App) Store() *store.Store { return a.store }

// Herdr exposes the runtime adapter.
func (a *App) Herdr() herdrx.Client { return a.herdr }

// Seeded lists files created by first-run setup during this process.
func (a *App) Seeded() []string { return a.seeded }

// Context is the fully resolved configuration for one working directory:
// global defaults, then global config, then project overrides.
type Context struct {
	Project project.Project
	Config  config.Config
	Roles   *roles.Set
}

// Resolve detects the project at dir and layers its optional overrides.
func (a *App) Resolve(dir string) (*Context, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = cwd
	}
	proj, err := project.Detect(a.git, dir)
	if err != nil {
		return nil, err
	}
	cfg, err := a.cfg.ApplyProject(proj.ConfigFile())
	if err != nil {
		return nil, err
	}
	roleSet, err := roles.Resolve(a.rolesDir, proj.RolesDir())
	if err != nil {
		return nil, err
	}
	return &Context{Project: proj, Config: cfg, Roles: roleSet}, nil
}

// Tasks lists tasks matching a filter.
func (a *App) Tasks(ctx context.Context, f store.Filter) ([]store.Task, error) {
	return a.store.List(ctx, f)
}

// ResolveTask finds one task from a human reference.
func (a *App) ResolveTask(ctx context.Context, ref string) (store.Task, error) {
	return a.store.Resolve(ctx, ref)
}
