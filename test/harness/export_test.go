package harness

import "github.com/modelcontextprotocol/go-sdk/mcp"

// FindLeaks and PlannedCalls expose internals to the tests in package
// harness_test, which import the fixture (the fixture imports this package).

func FindLeaks(f Fixture, text, where string) []Leak { return f.find(text, where) }

func PlannedCalls(tools []*mcp.Tool, f Fixture) []Call {
	var out []Call
	for _, p := range Plan(tools, f) {
		out = append(out, Call{Tool: p.tool, Args: p.args, Hostile: p.hostile})
	}
	return out
}
