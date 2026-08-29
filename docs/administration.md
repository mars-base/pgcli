# Administration

## Shell Completion

Enable tab completion for commands, flags, and instance names.

### Bash

```bash
# Linux
pg completion bash > /etc/bash_completion.d/pg

# macOS (with Homebrew bash-completion)
pg completion bash > $(brew --prefix)/etc/bash_completion.d/pg

# Or load in current session
source <(pg completion bash)
```

### Zsh

```bash
# Enable completion system (once)
echo "autoload -U compinit; compinit" >> ~/.zshrc

# Install completion
pg completion zsh > "${fpath[1]}/_pg"
```

### Fish

```bash
pg completion fish > ~/.config/fish/completions/pg.fish
```

### PowerShell

```powershell
pg completion powershell > pg.ps1
# Source from your PowerShell profile
```

## PostgreSQL Configuration

Modify PostgreSQL runtime parameters via `pg exec` with `ALTER SYSTEM`, then reload:

```bash
# Change a parameter
pg exec "ALTER SYSTEM SET work_mem = '256MB'"
pg exec "SELECT pg_reload_conf()"

# For a specific instance
pg exec -i proj01 "ALTER SYSTEM SET effective_cache_size = '4GB'"
pg exec -i proj01 "SELECT pg_reload_conf()"
```

**Note:** Some parameters (e.g. `shared_buffers`, `max_connections`) require a restart rather than reload. Use `pg stop && pg start` to apply those changes.

## Configuration File Management

Inspect or validate the config file (`~/.pgcli/pg.yaml` by default, override with `-c`).

```bash
# Show current configuration (YAML)
pg config show

# Show configuration as JSON
pg config show --json

# Validate config file structure
pg config validate
```

Init generates a default config; `--add` creates a named instance in the same file, `-o` writes to a custom path:

```bash
pg config init --add default --base-dir /data/pg
pg config init --add proj01 --base-dir /data/pg -o ./my-pg.yaml
```

Isolation parameters for running multiple configs on one host (see [Quick Start](quickstart.md) for planning):

```bash
pg config init --namespace t1 --pg-start-port 38000 --pg-ssh-port 43000 --add proj01 -o ~/.pgcli-t1/pg.yaml
```

| Parameter | Default | Meaning |
|-----------|---------|---------|
| `--namespace` | `default` | Prefix for container names: `pgcli-pg-<namespace>-<instance>`, backup container `pgcli-backup-<namespace>`. Pass `--namespace ""` to keep legacy names without a prefix |
| `--pg-start-port` | `35432` | First PG host port in the allocation range |
| `--pg-ssh-port` | `42201` | First SSH host port in the allocation range |

All three are saved into the config file (`namespace`, `pg_start_port`, `pg_ssh_port`); use disjoint port ranges across configs so allocations never collide.

## Destroy and Rebuild

```bash
# Destroy instance (keeps data directory)
pg destroy -i proj01

# Destroy without confirmation prompt
pg destroy -i proj01 --force

# Destroy with data cleanup (fresh start)
pg destroy -i proj01 --clean-data

# Recreate and start
pg create -i proj01 --base-dir /data/pg
pg start -i proj01
```

**Important:** Without `--clean-data`, the data directory is preserved. When restarting the instance, PostgreSQL uses the existing data and `init.sh` (which creates users) does **not** run again. This can cause issues if the data was created with a different user or schema.

Use `--clean-data` when you need a completely fresh instance, or when changing the default user in configuration.
