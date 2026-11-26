// Command kray gives an AI agent read-only access to a Kubernetes cluster
// over MCP (stdio), with secrets redacted and every call audited.
package main

import (
	"os"

	"github.com/groundcover-com/kray/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
