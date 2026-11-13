package main

import (
	"fmt"
	"os"

	"github.com/usingcoding/workdrive/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "workdrive:", err)
		os.Exit(1)
	}
}
