# Install kray

These steps work for you or for your agent. kray runs with your own kubeconfig credentials, so the steps create nothing in the cluster.

You need:

- A kubeconfig context for the cluster (`kubectl config current-context`).
- macOS or Linux, with `curl`. On Windows, download `kray_<version>_windows_amd64.exe` from the [releases](https://github.com/groundcover-com/kray/releases).

## 1. Install the binary

This downloads the latest release for your machine, checks its SHA256 sum, and puts it in `~/.local/bin`:

```bash
cd "$(mktemp -d)"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/groundcover-com/kray/releases/latest)
tag=${tag##*/}
file=kray_${tag#v}_${os}_${arch}
base=https://github.com/groundcover-com/kray/releases/download/$tag
curl -fsSL -O "$base/$file" -O "$base/SHA256SUMS"
grep " $file\$" SHA256SUMS | shasum -a 256 -c
mkdir -p ~/.local/bin && install -m 0755 "$file" ~/.local/bin/kray
~/.local/bin/kray version
```

If `~/.local/bin` isn't on your `PATH`, add it, or use the full path `~/.local/bin/kray` in the steps below. To check the cosign signature and the build provenance as well, see [SECURITY.md](../SECURITY.md#verify-a-release).

You can also install it with Go 1.26 or newer:

```bash
go install github.com/groundcover-com/kray@latest
```

## 2. Connect your agent

Claude Code:

```bash
claude mcp add --scope user kray -- kray serve
```

`--scope user` makes kray available in all your projects.

Cursor (`~/.cursor/mcp.json`), or any client that reads `mcpServers`. Add the `kray` entry to the file, and keep the servers that are already there:

```json
{ "mcpServers": { "kray": { "command": "kray", "args": ["serve"] } } }
```

Codex (`~/.codex/config.toml`):

```toml
[mcp_servers.kray]
command = "kray"
args = ["serve"]
```

Restart the agent so it loads the server. `kray serve` uses your current kubeconfig context and its credentials. To fix one cluster, add `--context NAME` after `serve`.

Whatever your credentials allow, kray sends only GET requests and never asks for Secret objects. It prints `not a read-only identity` when it starts. That's expected with your own credentials. The [read identity](#optional-create-a-read-identity) below removes the line.

### Approve the tools once

Every kray tool only reads, so your agent can call them without asking you each time. In Claude Code, add `mcp__kray` to `permissions.allow` in `~/.claude/settings.json`, and keep the entries that are already there:

```json
{ "permissions": { "allow": ["mcp__kray"] } }
```

In other clients, allow the six kray tools in the tool approval settings.

Don't do the same for `kubectl get`. `kubectl get secret -o yaml` is also a get, and it prints the secret values.

## 3. Try it

Ask your agent: "Is anything broken in this cluster?" The agent calls the `list` and `describe` tools.

On the command line, write `kray` where you'd write `kubectl`, for example `kray get pods -A`. See [Use kray](usage.md).

## Optional: create a read identity

Skip this step unless you want the cluster to enforce read access too. kray already reads only, in its own code. A read identity adds a second check in the API server: any process with its kubeconfig can only get and list the kinds kray reads, and it can't read Secret objects. The cluster audit log then shows kray's reads under their own name, apart from yours.

It costs four objects in the cluster: a Namespace, a ServiceAccount, a ClusterRole and a binding. You need the rights to create them, and the token expires after 8 hours.

First print the manifests. This applies nothing and needs no cluster access:

```bash
kray init --context my-cluster --dry-run
```

**Agents: show these manifests to the person and ask before the next command.** It creates objects in the cluster.

```bash
kray init --context my-cluster --yes
```

Then check the result:

```bash
kray doctor
```

After `init`, `kray serve` uses the read identity. Its context is `kray@my-cluster`, so if your agent config has `--context my-cluster`, change it to `--context kray@my-cluster` or remove it. Run `kray init --refresh` when the token expires. See [Security model](security-model.md) for what the identity can read.

To remove the identity:

```bash
kubectl delete clusterrolebinding,clusterrole kray-reader
kubectl delete namespace kray
rm ~/.config/kray/kubeconfig
```

Without that file, kray goes back to your own credentials.
