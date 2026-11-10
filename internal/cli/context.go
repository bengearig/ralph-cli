package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// findProject locates the project from --project-dir or the working
// directory. The result, including ErrNotFound, is memoized.
func (a *app) findProject() (project.Project, error) {
	if a.proj != nil || a.projErr != nil {
		return derefProject(a.proj), a.projErr
	}
	var p project.Project
	var err error
	if a.projectDir != "" {
		p, err = project.At(a.projectDir)
	} else {
		var wd string
		wd, err = os.Getwd()
		if err == nil {
			p, err = project.Locate(wd)
		}
	}
	if err != nil {
		a.projErr = err
		return project.Project{}, err
	}
	a.proj = &p
	return p, nil
}

func derefProject(p *project.Project) project.Project {
	if p == nil {
		return project.Project{}
	}
	return *p
}

// loadConfig merges all configuration layers. Outside a project the project
// layer is simply absent, so commands like `config show` still work.
func (a *app) loadConfig() (*config.Loaded, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	src := config.Sources{
		ExtraFile: a.configFile,
		Environ:   os.Environ(),
		Flags:     a.root.PersistentFlags(),
	}
	if global, err := config.GlobalPath(); err == nil {
		src.GlobalFile = global
	}
	p, err := a.findProject()
	switch {
	case err == nil:
		src.ProjectFile = p.ConfigPath()
	case !errors.Is(err, project.ErrNotFound):
		return nil, err
	}
	l, err := config.Load(src)
	if err != nil {
		return nil, err
	}
	a.cfg = l
	a.log = newLogger(a.streams, l.Config.Log)
	return l, nil
}

func (a *app) logger() *slog.Logger {
	if a.log == nil {
		return slog.New(slog.NewTextHandler(a.streams.Err, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return a.log
}

func newLogger(s Streams, c config.LogConfig) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.Level)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if c.Format == "json" {
		return slog.New(slog.NewJSONHandler(s.Err, opts))
	}
	return slog.New(slog.NewTextHandler(s.Err, opts))
}

// service opens the project's store and search index, closing them when the
// command finishes.
func (a *app) service(ctx context.Context) (*service.Service, error) {
	if a.svc != nil {
		return a.svc, nil
	}
	p, err := a.findProject()
	if err != nil {
		return nil, err
	}
	l, err := a.loadConfig()
	if err != nil {
		return nil, err
	}
	tm, err := service.NewTaskManager(l.Config)
	if err != nil {
		a.tmErr = err
		fmt.Fprintf(a.streams.Err, "task manager disabled: %v\n", err)
	}
	svc, closeFn, err := service.OpenWithTaskManager(ctx, l.Config, p.Root, a.logger(), tm)
	if err != nil {
		return nil, err
	}
	a.closers = append(a.closers, closeFn)
	a.svc = svc
	return svc, nil
}
