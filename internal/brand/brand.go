// Package brand holds the product name. It is the rename point: every
// user-visible name (binary help, MCP server name, config and audit paths,
// RBAC object names, User-Agent) derives from Name. A guard test fails when a
// string literal outside this package contains the name.
package brand

// Name is the product name. Change it here to rename the product.
const Name = "kray"

// Version is set at build time with -ldflags "-X .../internal/brand.Version=...".
var Version = "dev"

// UserAgent is the HTTP User-Agent sent to the Kubernetes API server.
func UserAgent() string { return Name + "/" + Version }
