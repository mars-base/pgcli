# pgcli Core Functionality Test Report

**Test Date:** 2026-08-27
**Test Environment:** Debian 12 (bookworm), linux/amd64, VM at 10.241.20.147
**pgcli Version:** v1.0.0
**PostgreSQL Version:** 18.6
**pgBackRest Version:** 2.59.1
**Podman Version:** 4.9.3 (via podman-launcher)

---

## Test Results

| # | Feature | Command | Result | Notes |
|---|---------|---------|--------|-------|
| 1 | Version | `pg version` | PASS | v1.0.0 (built 2026-08-26T09:06:54Z) |
| 2 | Config Init | `pg config init --add default --base-dir /tmp/pg-test` | PASS | Config generated at ~/.pgcli/pg.yaml |
| 3 | Create Instance | `pg create -i test01 --base-dir /tmp/pg-test` | PASS | Auto password, container name, stanza |
| 4 | Start Default | `pg start` | PASS | Port 35432, tuning applied, restart for shared_buffers |
| 5 | Start Second Instance | `pg start -i test01` | PASS | Port 35433, auto port assignment |
| 6 | List Instances | `pg list` | PASS | Shows both instances with status |
| 7 | Status | `pg status` | PASS | Container status, connection string, backup info |
| 8 | Exec SQL | `pg exec "SELECT version()"` | PASS | Returns PostgreSQL version |
| 9 | Exec Multi-Statement | CREATE TABLE + INSERT + SELECT | PASS | Table creation, data insertion, query |
| 10 | ALTER SYSTEM + Reload | `pg exec "ALTER SYSTEM SET work_mem = '256MB'"` + reload | PASS | work_mem verified 256MB |
| 11 | Snapshot Create | `pg snapshot create -i test01` | PASS | Full backup 20260827-025524F |
| 12 | Snapshot List | `pg snapshot list -i test01` | PASS | Shows backup name, type, timestamps |
| 13 | Snapshot Delete (only full) | `pg snapshot delete 20260827-025524F -i test01` | PASS (correctly rejected) | pgBackRest protects the only full backup |
| 14 | Snapshot Delete (non-existent) | `pg snapshot delete 20260827-999999F -i test01` | FAIL | Reported success (v1.0.0 bug, fixed in 9640bbe) |
| 15 | Destroy + Clean Data | `pg destroy -i test01 --clean-data --force` | PASS | Container, data, backup all removed |
| 16 | PITR Restore | `pg restore --time "2026-08-27 10:57:25+08" --promote --force` | PASS | Restored to exact point in time |
| 17 | PITR Data Verification | `SELECT * FROM test_restore` | PASS | before_backup + after_backup_1 present, after_backup_2 correctly excluded |
| 18 | Stop Instance | `pg stop` | PASS | Container exited cleanly |

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

| Issue | Status | Fix Commit |
|-------|--------|------------|
| Deleting non-existent snapshot reports success | Fixed | 9640bbe (pending release) |
| WAL archive may lag behind real time | By design | pg restore validates WAL coverage before restoring |

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
