package wtree

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const usage = "Usage: wtree up --port <1..65535> [--slug <slug>] | down [--slug <slug>] | status | version\n"

// Run executes one CLI invocation; status and help do not require a container engine.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, deps Dependencies) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "wtree:", err); return 1 }
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if args[0] != "up" && args[0] != "down" && args[0] != "status" {
		fmt.Fprint(stderr, usage)
		return fail(fmt.Errorf("unknown command %q", args[0]))
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	slug := flags.String("slug", "", "route slug")
	port := flags.String("port", "", "upstream port")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		return fail(errors.New("unexpected positional arguments"))
	}
	if args[0] == "status" && (*slug != "" || *port != "") {
		return fail(errors.New("status takes no options"))
	}
	if args[0] == "down" && *port != "" {
		return fail(errors.New("down does not accept --port"))
	}
	var p uint64
	if args[0] == "up" {
		var err error
		p, err = strconv.ParseUint(*port, 10, 16)
		if err != nil || p == 0 {
			return fail(errors.New("--port must be a number from 1 to 65535"))
		}
	}
	if deps.Registry == nil {
		return fail(errors.New("route registry is not configured"))
	}
	if args[0] == "status" {
		routes, err := deps.Registry.List(ctx)
		if err != nil {
			return fail(err)
		}
		for _, r := range routes {
			fmt.Fprintf(stdout, "https://%s.wtree.dev\t127.0.0.1:%d\t%s\n", r.Slug, r.Port, r.Worktree)
		}
		return 0
	}
	var root, branch string
	if args[0] == "up" || *slug == "" {
		if deps.Git == nil {
			return fail(errors.New("Git is not configured"))
		}
		var err error
		root, branch, err = deps.Git.Worktree(ctx)
		if err != nil {
			return fail(err)
		}
	}
	if args[0] == "up" {
		resolved, err := ResolveSlug(*slug, branch)
		if err != nil {
			return fail(err)
		}
		if err = addRoute(ctx, deps, Route{Slug: resolved, Port: uint16(p), Worktree: root}); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "https://%s.wtree.dev\n", resolved)
		return 0
	}
	if *slug != "" {
		var err error
		*slug, err = ResolveSlug(*slug, "")
		if err != nil {
			return fail(err)
		}
	}
	removed, err := removeRoute(ctx, deps, *slug, root)
	if err != nil {
		return fail(err)
	}
	if removed {
		fmt.Fprintln(stdout, "Route removed")
	} else {
		fmt.Fprintln(stdout, "No matching route")
	}
	return 0
}

type ExecGit struct{}

func (ExecGit) Worktree(ctx context.Context) (string, string, error) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("find Git worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	root, err := filepath.Abs(strings.TrimSpace(string(out)))
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve Git worktree: %w", err)
	}
	out, err = exec.CommandContext(ctx, "git", "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return root, "", nil
		}
		return "", "", fmt.Errorf("find Git branch: %w", err)
	}
	return root, strings.TrimSpace(string(out)), nil
}

func DefaultDependencies() (Dependencies, error) {
	p, err := ConfigPaths()
	if err != nil {
		return Dependencies{}, err
	}
	for _, v := range []*string{&p.Dir, &p.Cert, &p.Key, &p.Registry, &p.Config} {
		*v, err = filepath.Abs(*v)
		if err != nil {
			return Dependencies{}, err
		}
	}
	r, err := OpenRegistry(p.Registry)
	if err != nil {
		return Dependencies{}, err
	}
	return Dependencies{Paths: p, Registry: r, Git: ExecGit{}, Engine: func(ctx context.Context) (LifecycleEngine, error) {
		e, err := SelectEngine(ctx)
		if err != nil {
			return nil, err
		}
		return &e, nil
	}}, nil
}
