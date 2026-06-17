package main

import (
	"fmt"
	"os"

	"github.com/rancher/tests/cmd/agentic-qa/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
