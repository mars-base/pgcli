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

## Destroy and Rebuild

```bash
# Destroy instance (keeps data directory)
pg destroy -i proj01

# Destroy with data cleanup (fresh start)
pg destroy -i proj01 --clean-data

# Recreate and start
pg create -i proj01 --base-dir /data/pg
pg start -i proj01
```

**Important:** Without `--clean-data`, the data directory is preserved. When restarting the instance, PostgreSQL uses the existing data and `init.sh` (which creates users) does **not** run again. This can cause issues if the data was created with a different user or schema.

Use `--clean-data` when you need a completely fresh instance, or when changing the default user in configuration.
