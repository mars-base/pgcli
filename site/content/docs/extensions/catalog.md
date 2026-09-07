---
title: "Extension Catalog"
description: "Built-in PostgreSQL extensions available in the base image"
weight: 10
---


This page lists all built-in extensions available in the base image. For usage examples, see [Popular Extensions](./popular/).

## Built-in Extensions (45)

These extensions ship in the PostgreSQL base image — no image build or `pg extension install` needed. Just run `CREATE EXTENSION` to enable them.

### Requires shared_preload_libraries

These extensions need a PostgreSQL restart after enabling. pgcli handles this automatically when installed via `pg extension install`.

| Extension | Description |
|-----------|-------------|
| `pg_stat_statements` | SQL performance analysis — tracks query execution counts, timing, and resource usage |
| `pg_cron` | Cron-based job scheduler inside the database |
| `pg_hint_plan` | Query hints to influence the planner |
| `pg_stat_monitor` | Advanced performance monitoring with query plans |
| `pg_qualstats` | Query predicate statistics for index recommendations |
| `pg_stat_kcache` | Kernel-level CPU/IO performance stats |
| `pg_wait_sampling` | Wait event sampling for performance analysis |
| `pg_track_settings` | Track configuration changes over time |
| `timescaledb` | Time-series database extension with hypertables |
| `pg_prewarm` | Preload table data into shared buffers after restart |

### No restart needed

These extensions can be enabled instantly with `CREATE EXTENSION` — no restart required.

| Extension | Description |
|-----------|-------------|
| `uuid-ossp` | UUID generation functions (v1/v3/v4/v5) |
| `hstore` | Key-value pair storage in a single column |
| `pgcrypto` | Hashing (bcrypt/sha256), encryption/decryption, random generation |
| `pg_trgm` | Trigram similarity matching for fuzzy search |
| `unaccent` | Strip accents from characters for international text search |
| `fuzzystrmatch` | Fuzzy string matching (Levenshtein, Soundex, Metaphone) |
| `intarray` | Integer array operations (unique, sort, intersection) |
| `isn` | ISBN/ISSN/EAN standard number types with validation |
| `pg_repack` | Online table reorganization without exclusive locks |
| `pg_squeeze` | Table space reclamation |
| `pg_partman` | Automatic partition management |
| `pgvector` (Pigsty) | Vector similarity search for AI applications |
| `postgis` (Pigsty) | Geospatial data support |
| `pgmq` (Pigsty) | Lightweight message queue |
| `tablefunc` | Crosstab / pivot table functions |
| `btree_gist` | B-tree index support for GiST indexes |
| `btree_gin` | B-tree index support for GIN indexes |
| `citext` | Case-insensitive text type |
| `cube` | Multi-dimensional cube data type |
| `ltree` | Hierarchical label tree data type |
| `seg` | Floating-point interval data type |
| `earthdistance` | Great-circle distance calculations on Earth's surface |
| `postgres_fdw` | Query external PostgreSQL servers as local tables |
| `file_fdw` | Read server files as foreign tables |
| `dblink` | Connect to other PostgreSQL databases |
| `amcheck` | Verify B-tree index integrity |
| `pageinspect` | Low-level page inspection for debugging |
| `pg_buffercache` | View shared buffer cache contents |
| `pg_freespacemap` | View free space map for tables |
| `pg_visibility` | View visibility map for tables |
| `pg_walinspect` | Inspect WAL records |
| `pgstattuple` | Table-level tuple statistics |
| `pgrowlocks` | Show row-level locking information |
| `pg_surgery` | Low-level heap tuple manipulation |
| `xml2` | XML parsing and XPath functions |
| `dict_int` | Integer text search dictionary |
| `dict_xsyn` | Extended synonym text search dictionary |
| `bloom` | Bloom filter index access method |
| `autoinc` | Auto-increment trigger function |
| `insert_username` | Insert username trigger function |
| `moddatetime` | Auto-update timestamp trigger |
| `refint` | Referential integrity trigger functions |
| `tcn` | Triggered change notification |
| `sslinfo` | SSL connection information functions |
| `lo` | Large object type support |
| `intagg` | Integer array aggregate/enumeration |
| `tsm_system_rows` | Table sampling by row count |
| `tsm_system_time` | Table sampling by time |
| `pg_logicalinspect` | Logical replication slot inspection |
