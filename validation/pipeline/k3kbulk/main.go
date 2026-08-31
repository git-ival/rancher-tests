package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rancher/tests/actions/k3k"
)

func main() {
	configPath := flag.String("config", "k3k-bulk.yaml", "path to the batch configuration")
	timeout := flag.Duration("timeout", 45*time.Minute, "overall operation timeout")
	flag.Parse()
	if flag.NArg() != 1 || (flag.Arg(0) != "create" && flag.Arg(0) != "delete") {
		fmt.Fprintln(os.Stderr, "usage: k3kbulk [flags] create|delete")
		os.Exit(2)
	}

	config, err := k3k.LoadConfig(*configPath)
	if err != nil {
		fatal(err)
	}
	rancherClient, err := k3k.NewRancherClient(config.Rancher)
	if err != nil {
		fatal(fmt.Errorf("create Rancher client: %w", err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := rancherClient.DiscoverTargets(ctx, config); err != nil {
		fatal(err)
	}
	state, err := k3k.LoadOrCreateState(config)
	if err != nil {
		fatal(err)
	}

	orchestrator := &k3k.Orchestrator{
		Config: config,
		State:  state,
		CLI: k3k.CLI{
			K3KCLI:  config.K3KCLI,
			Kubectl: config.Kubectl,
			Runner:  k3k.ExecRunner{},
		},
		Rancher: rancherClient,
	}
	if flag.Arg(0) == "create" {
		err = orchestrator.Create(ctx)
	} else {
		err = orchestrator.Delete(ctx)
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
