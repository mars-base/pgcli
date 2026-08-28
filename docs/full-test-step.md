# pgcli Core Functionality Test Report

**Test Date:** 2026-08-27 (initial), 2026-08-28 (additional)
**Test Environment:** Debian 12 (bookworm), linux/amd64, VM + local
**pgcli Version:** v1.3.3 (clone feature; e2e automated suite)
**PostgreSQL Version:** 18.6
**pgBackRest Version:** 2.59.1
**Podman Version:** 4.9.3 (via podman-launcher)

---

## Test Results

| # | Feature | Command | Result | Notes |
|---|---------|---------|--------|-------|
| 1 | Version | `pg version` | PASS | v1.0.1 (built 2026-08-27T03:04:42Z) |
| 2 | Config Init | `pg config init --add default --base-dir /tmp/pg-test` | PASS | Config generated at ~/.pgcli/pg.yaml |
| 3 | Create Instance | `pg create -i test01 --base-dir /tmp/pg-test` | PASS | Auto password, container name, stanza |
| 4 | Start Default | `pg start` | PASS | Port 35432, tuning applied, restart for shared_buffers |
| 5 | Start Second Instance | `pg start -i test01` | PASS | Port 35433, auto port assignment |
| 6 | List Instances | `pg list` | PASS | Shows both instances with status |
| 7 | Status | `pg status` | PASS | Container status, connection string, backup info |
| 8 | Exec SQL | `pg exec "SELECT version()"` | PASS | Returns PostgreSQL version |
| 9 | Exec Multi-Statement | CREATE TABLE + INSERT + SELECT | PASS | Table creation, data insertion, query |
| 10 | ALTER SYSTEM + Reload | `pg exec "ALTER SYSTEM SET work_mem = '256MB'"` + reload | PASS | work_mem verified 256MB |
| 11 | Snapshot Create (full) | `pg snapshot create -i test01` | PASS | Full backup 20260827-025524F |
| 12 | Snapshot List | `pg snapshot list -i test01` | PASS | Shows backup name, type, timestamps |
| 13 | Snapshot Delete (only full) | `pg snapshot delete 20260827-025524F -i test01` | PASS (correctly rejected) | "it is the only full backup; create another full backup first" |
| 14 | Snapshot Delete (non-existent) | `pg snapshot delete 20260827-999999F -i test01` | PASS (correctly rejected) | "snapshot 20260827-999999F not found" |
| 15 | Snapshot Create (diff) | `pg snapshot create --type diff -i default` | PASS | Diff backup 20260827-025711F_20260827-030625D |
| 16 | Snapshot Delete (diff) | `pg snapshot delete 20260827-025711F_20260827-030625D -i default` | PASS | Successfully deleted diff snapshot |
| 17 | Snapshot Create (incr) | `pg snapshot create --type incr -i default` | PASS | Incr backup 20260827-025711F_20260827-030640I |
| 18 | Snapshot Delete (incr) | `pg snapshot delete 20260827-025711F_20260827-030640I -i default` | PASS | Successfully deleted incr snapshot |
| 19 | Destroy + Clean Data | `pg destroy -i test01 --clean-data --force` | PASS | Container, data, backup all removed |
| 20 | PITR Restore | `pg restore --time "2026-08-27 10:57:25+08" --promote --force` | PASS | Restored to exact point in time |
| 21 | PITR Data Verification | `SELECT * FROM test_restore` | PASS | before_backup + after_backup_1 present, after_backup_2 correctly excluded |
| 22 | Stop Instance | `pg stop` | PASS | Container exited cleanly |
| 23 | psql One-Shot | `pg psql -- -c "SELECT 1"` | PASS | psql passthrough args |
| 24 | psql from stdin | `echo "SELECT 1;" \| pg psql` | PASS | Non-interactive script mode |
| 25 | psql --dsn | `pg psql --dsn postgres://... "SELECT 1"` | PASS | Remote connection via temporary container |
| 26 | Container Shell | `pg shell -- -c "whoami"` | PASS | Root shell inside container |
| 27 | Export Custom | `pg export -i pg01 -o backup.dump` | PASS | Custom format (PGDMP magic bytes) |
| 28 | Export SQL Format | `pg export -i pg01 -o backup.sql` | PASS | Plain SQL (psql-compatible) |
| 29 | Export Gzip | `pg export -i pg01 -o backup.dump.gz` | PASS | Auto-detected from .gz extension |
| 30 | Import File | `pg import -i pg02 backup.dump` | PASS | Custom format restore |
| 31 | Import --clean | `pg import -i pg02 backup.dump --clean` | PASS | Drops objects before restore |
| 32 | exec --dsn | `pg exec --dsn postgres://... "SELECT 1"` | PASS | Remote SQL execution |
| 33 | Pipe Between Instances | `pg export -i pg01 \| pg import -i pg02 --clean` | PASS | No temp file, streamed |
| 34 | Pipe via SSH | `pg export -i pg01 \| ssh host "pg import -i pg02"` | PASS | Cross-host streaming |
| 35 | Pipe Local to VM | `pg export --dsn <local> \| pg import --dsn <vm> --clean` | PASS | 10000 rows transferred intact |
| 36 | Pipe VM to Local | `pg export --dsn <vm> \| pg import --dsn <local> --clean` | PASS | Reverse direction verified |
| 37 | DSN/Instance Conflict | `pg exec --dsn ... -i pg02 ...` | PASS (correctly rejected) | "--dsn and --instance are mutually exclusive" |
| 38 | Config Show/Validate | `pg config show` / `pg config validate` | PASS | YAML output, validation OK |
| 39 | Shell Completion | `pg completion bash/zsh/fish/powershell` | PASS | Scripts generated |
| 40 | Backup Container | `pg backup setup/start/stop/status` | PASS | Container lifecycle, 2 stanzas, snapshot verified after rebuild |
| 41 | Clone Local | `pg clone test04 -i pg01` | PASS | 510000 rows cloned, auto port/password |
| 42 | Clone --dsn | `pg clone test05 --dsn postgres://...` | PASS | Remote-to-local clone, progress shown |
| 43 | Clone Pre-Check | stopped source / wrong DSN password | PASS (correctly rejected) | Fails before creating anything |
| 44 | Export File Validity | `head -c 4` = PGDMP / SQL contains CREATE TABLE / `gzip -t` | PASS | All three formats verified valid |
| 45 | Import Data Verify | `SELECT count(*)` after import = 3 | PASS | custom / SQL / gzip formats |
| 46 | Pipe Data Verify | `pg export -i e2e-test \| pg import -i e2e-test2 --clean` | PASS | count = 3 |
| 47 | Pipe --dsn Data Verify | `pg export --dsn <dsn1> \| pg import --dsn <dsn2> --clean` | PASS | count = 3 |
| 48 | Clone Target Exists | `pg clone e2e-test -i e2e-test` | PASS (correctly rejected) | "already exists in config" |
| 49 | Clone Source Missing | `pg clone e2e-bad2 -i nosuch-inst` | PASS (correctly rejected) | "not found in config" |
| 50 | Clone --dsn + -i Conflict | `pg clone e2e-x --dsn <dsn> -i e2e-test` | PASS (correctly rejected) | "mutually exclusive" |
| 51 | psql --dsn + -i Conflict | `pg psql --dsn <dsn> -i e2e-test -- -c "SELECT 1"` | PASS (correctly rejected) | "mutually exclusive" |
| 52 | export --dsn + -i Conflict | `pg export --dsn <dsn> -i e2e-test -o x.dump` | PASS (correctly rejected) | "mutually exclusive" |
| 53 | import --dsn + -i Conflict | `pg import --dsn <dsn> -i e2e-test f.dump` | PASS (correctly rejected) | "mutually exclusive" |
| 54 | exec --dsn Container Cmd | `pg exec --dsn <dsn> -- -c "whoami"` | PASS (correctly rejected) | "only supports SQL mode" |
| 55 | export --dsn to File | `pg export --dsn <dsn> -o dsn-export.dump` | PASS | PGDMP magic bytes verified |
| 56 | import --dsn from File | `pg import --dsn <dsn2> dsn-export.dump --clean` | PASS | count = 3 |

---

## PITR Verification Detail

```
Setup:
  1. CREATE TABLE test_restore(id serial, val text)
  2. INSERT 'before_backup'
  3. pg snapshot create (full backup at 10:57:11 UTC)
  4. INSERT 'after_backup_1'
  5. sleep 2s
  6. INSERT 'after_backup_2'
  7. pg restore --time "2026-08-27 10:57:25+08" --promote

Result:
  id |      val
 ----+----------------
   1 | before_backup
   2 | after_backup_1
  (2 rows)

  after_backup_2 correctly excluded (inserted after target time)
```

---

## Known Issues

All known issues have been fixed in v1.0.1:

| Issue | Status | Fix Commit |
|-------|--------|------------|
| Deleting non-existent snapshot reports success | **Fixed in v1.0.1** | 9640bbe |
| Deleting only full backup unclear error | **Fixed in v1.0.1** | 2ebff3f |
| WAL archive may lag behind real time | By design | pg restore validates WAL coverage before restoring |
| podman name filter substring match: false "not running" with multiple instances | **Fixed in v1.3.3** | 519506d |
| Clone steals the port of a stopped instance | **Fixed in v1.3.3** | 519506d |
| e2e cleanup fails on rootless container files (permission denied) | **Fixed in v1.3.3** | 519506d |

---

## Install Script Test

| Feature | Result |
|---------|--------|
| Auto-detect sudo (sudo -n true) | PASS - installs to /usr/local/bin |
| podman-launcher install to /usr/local/bin | PASS |
| Trigger podman binary download | PASS - podman 4.9.3 |
| uidmap auto-install | PASS - apt install uidmap |
| policy.json auto-create | PASS - created via sudo |
| User namespace check | PASS - enabled |
| Container image pre-pull | PASS - both images pulled |
