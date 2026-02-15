# Shell Completions

mcpmu supports tab-completion for bash, zsh, fish, and PowerShell. Completions dynamically resolve server names, namespace names, and subcommand arguments from your config.

## Zsh

```bash
# If installed via Homebrew
mcpmu completion zsh > "$(brew --prefix)/share/zsh/site-functions/_mcpmu"

# Or using your fpath directly
mcpmu completion zsh > "${fpath[1]}/_mcpmu"
```

Then restart your shell or run `exec zsh`.

If completions aren't working, ensure `compinit` is called in your `.zshrc`:
```bash
autoload -Uz compinit && compinit
```

## Bash

```bash
# System-wide (requires sudo)
mcpmu completion bash > /etc/bash_completion.d/mcpmu

# Per-user
mkdir -p ~/.local/share/bash-completion/completions
mcpmu completion bash > ~/.local/share/bash-completion/completions/mcpmu
```

Requires the `bash-completion` package. Install via `brew install bash-completion@2` on macOS.

## Fish

```bash
mcpmu completion fish > ~/.config/fish/completions/mcpmu.fish
```

## PowerShell

```powershell
mcpmu completion powershell > mcpmu.ps1
# Then source it in your profile
. ./mcpmu.ps1
```

## What gets completed

| Command | Arg 1 | Arg 2 | Arg 3 | Arg 4 |
|---------|-------|-------|-------|-------|
| `remove` | server | | | |
| `rename` | server | | | |
| `mcp login` | HTTP server | | | |
| `mcp logout` | HTTP server | | | |
| `namespace remove` | namespace | | | |
| `namespace default` | namespace | | | |
| `namespace rename` | namespace | | | |
| `namespace assign` | namespace | server | | |
| `namespace unassign` | namespace | server | | |
| `namespace set-deny-default` | namespace | true/false | | |
| `permission list` | namespace | | | |
| `permission set` | namespace | server | | allow/deny |
| `permission unset` | namespace | server | | |
| `serve --namespace` | namespace | | | |
| `serve --log-level` | level | | | |