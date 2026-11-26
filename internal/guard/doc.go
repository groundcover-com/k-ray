// Package guard holds tests that enforce the rules in AGENTS.md on the
// source code itself: no os/exec, no listeners, one rest.Config builder, no
// path from the MCP server to the setup code, and no product name in string
// literals outside internal/brand.
package guard
