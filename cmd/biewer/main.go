// Command biewer is a local resource supervisor for Claude Code, Codex, and
// other coding agents. See `biewer help` or the project README.
package main

import (
	"os"

	"github.com/shaktsin/biewer/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
