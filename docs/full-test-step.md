# pgcli Core Functionality Test Report

**Test Date:** 2026-08-27
**Test Environment:** Debian 12 (bookworm), linux/amd64, VM at 10.241.20.147
**pgcli Version:** v1.0.1
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
