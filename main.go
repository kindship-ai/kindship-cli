package main

import (
	"os"

	"github.com/kindship-ai/kindship-cli/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
