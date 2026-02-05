# Security Policy

## Reporting Vulnerabilities

If you discover a security vulnerability, please report it responsibly:

- **GitHub Security Advisories**: Use the [Security tab](../../security/advisories/new) to report privately.
- **Email**: Send details to security@mcpmu.uk

Please do **not** open public issues for security vulnerabilities.

We aim to acknowledge reports within 48 hours and provide a fix or mitigation plan within 7 days.

## Security Considerations

### Config and credential files

mcpmu stores configuration in `~/.config/mcpmu/`. All files are created with restrictive permissions:

- **Config files**: `0600` (owner read/write only)
- **Directories**: `0700` (owner only)

Do not relax these permissions, as config files may reference environment variables containing secrets (e.g. bearer tokens).

### OAuth credential storage

mcpmu supports OAuth for HTTP-based MCP servers. Credentials are stored using the system keyring when available, which is the preferred and more secure option. File-based token storage is used as a fallback.

### Process execution

mcpmu spawns subprocesses defined in its configuration. Only add servers from sources you trust. Review the `command` and `args` fields in your config before running.
