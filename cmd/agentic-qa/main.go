package main

import (
	"os"

	"github.com/rancher/tests/cmd/agentic-qa/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
