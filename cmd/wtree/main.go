package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/darianbr/worktree/internal/wtree"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println("wtree", version)
		return
	}
	deps, err := wtree.DefaultDependencies()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wtree:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(wtree.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, deps))
}
