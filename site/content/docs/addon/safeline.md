---
title: "SafeLine WAF"
description: "Run SafeLine (雷池) Community Edition as a managed addon — a seven-container stack (bundled PostgreSQL 15, HTTPS console, detector, tengine traffic proxy and support services) on a dedicated static-IP bridge network, Linux rootful only"
weight: 70
---

`pg addon install safeline` runs **SafeLine (雷池) Community Edition** as a managed addon. It is a **top-level addon** (it does not front a PostgreSQL instance) and it is a **seven-container stack per instance**:

- **postgres** — the stack's own bundled PostgreSQL 15.18, pinned separately from pgcli instances (upstream has not validated PostgreSQL 18 yet, see [chaitin/SafeLine#1250](https://github.com/chaitin/SafeLine/issues/1250))
- **mgt** — the HTTPS console, the only published port
- **detector** — the attack-detection engine
- **tengine** — the traffic reverse proxy, protecting the sites you configure in the console
- **luigi**, **fvm**, **chaos** — supporting services

> **tengine uses host networking.** The ports of the sites you protect are bound **directly on the host** (80/443 by default) and must be free. The console itself is HTTPS with a self-signed certificate.

**Requirements:** Linux, **rootful podman**, at least 2 GB of RAM. The stack pins every bridge container to a fixed address inside a dedicated `/24`, which rootless podman cannot do; the addon refuses to install on macOS/Windows or rootless hosts.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `safeline` | Instance name (map key, container/network prefix and default subcommand name) |
| `--version` | `9.4.7` | SafeLine release tag: pulls `docker.io/chaitin/safeline-{mgt,detector,tengine,luigi,fvm,chaos}:<version>` |
| `--postgres-version` | `15.18` | Bundled PostgreSQL tag: pulls `docker.io/chaitin/safeline-postgres:<version>` |
| `--subnet-prefix` | `172.22.222` | First three octets of the stack's private `/24` bridge subnet. Each stack needs its own subnet; addresses are fixed above the gateway: `.1` gateway, `.2` postgres, `.4` mgt, `.5` detector, `.7` luigi, `.8` fvm, `.10` chaos |
| `--port` | auto (`9443`) | Console HTTPS host port. `0` = auto-assign from the SafeLine pool (`safeline_start_port`, default `9443`) |
| `--listen` | `0.0.0.0` | Console bind address, matching the upstream compose default. Set `127.0.0.1` to keep the console off the network |
| `--data-dir` | `<base_dir>/addon/safeline/<name>` | Stack data root (bundled database, console state, nginx config, logs). Must be absolute except for `base_dir`-relative paths |
| `--force` | false | Recreate the seven containers, keeping the data directory, and rotate the console admin password |

`--image`, `--dsn`, `--pg-name`, `--password`, `--config-option`, `--config-file`, `--tls`, `--tls-pem`, `--tls-cert`, `--tls-key`, `--advertise-host`, `--cluster`, `--peer` and `--join` do not apply to SafeLine and are rejected: the stack bundles its own PostgreSQL and every image is pinned by `--version`/`--postgres-version`.

## Install

```bash
pg addon install safeline --name waf1
```

The install pulls the seven images if needed, creates the directories and the dedicated bridge network, generates the bundled-database password, starts the containers in dependency order (postgres → mgt console → detector → fvm → chaos → luigi → tengine), waits for PostgreSQL, the console health endpoint and tengine's control socket, captures the console admin password from the `resetadmin` bootstrap, saves `pg.yaml`, and prints a summary:

```text
✓ SafeLine WAF "waf1" installed
  Console:     https://127.0.0.1:9443 (self-signed certificate)
  Admin:       admin
  Password:    <generated> (also stored: `pg addon password safeline --name waf1`)
  Version:     SafeLine 9.4.7, PostgreSQL 15.18
  Subnet:      172.22.222.0/24 (gateway 172.22.222.1)
  Containers:  postgres, mgt, detector, luigi, fvm, chaos (bridge pgcli-safeline-waf1); tengine (host network)
  Data dir:    /home/you/.pgcli/addon/safeline/waf1
  Traffic:     tengine listens on host networking — add protected sites in the console;
               their entry ports default to 80/443 and must be free on this host
```

The install also generates the internal detector management token (`detector_mgmt_token` in `pg.yaml`) and passes it to the detector, fvm and luigi containers. The detector image authenticates management-API calls by default, and the console publishes site rules through that API — without the shared token, creating a site fails. A stack installed by an older pgcli is migrated on its next `pg addon start`, which generates the token and recreates those containers.

A second instance needs its own subnet and console port (the pool advances automatically):

```bash
pg addon install safeline --name waf2 --subnet-prefix 172.22.223 --port 9444
```

Re-running the same install with no changed flags is a no-op; changing any stored value (version, subnet, port, listen, data dir) without `--force` is refused instead of silently ignored.

## Console

Open `https://<listen>:<port>` (default `https://<host>:9443`) and log in as `admin` with the generated password. Retrieve it later with:

```bash
pg addon password safeline --name waf1
```

The password is captured from the mgt container's `resetadmin` bootstrap, which **rotates the admin password on every call**. pgcli runs it only when no password is recorded (install, or a later `start` if the install failed short) and on `--force` re-installs. To rotate it yourself:

```bash
pg addon password safeline --name waf1 --reset
```

The command reruns the bootstrap, saves the new `admin_password` back to `pg.yaml` and prints it, so `pg addon password safeline --name waf1` keeps returning the current password. The stack must be running.

The console serves HTTPS with a self-signed certificate; browsers show the usual warning.

## Protected sites and traffic ports

Applications (protected sites) are configured **in the console** or, for scripting, through `pg addon safeline site` (below). Each site gets an entry port on the host — 80/443 by default — served by the host-networked **tengine** container. Before adding a site, make sure the port is free on the host (`ss -tlnp`). In the firewall, open the console port and each site's entry port (80 and 443 by default).

A site's upstream can be an IP or a host name (for example a load-balancer domain). tengine resolves host names from the host's DNS **when its config loads**, so after the load balancer's DNS records change, run `pg addon reload safeline --name waf1` to re-test and gracefully reload the tengine config — the new addresses take effect without restarting the stack or dropping in-flight requests.

Ansible deployments can declare sites in the `sites` field of a `type: safeline` entry (each run creates missing sites and updates changed upstreams; certificates are uploaded from controller file paths) — see the [Ansible add-ons chapter](../ansible/addons.md#safeline-sites).

### Managing sites from the CLI

`pg addon safeline site ...` drives the console's own web API with the stored admin password — the same encrypted login and `/api/open/site` calls the browser makes — so sites can be scripted without the UI. The stack must be running.

> **Deployed under a specific account?** These commands read the invoking user's `~/.pgcli/pg.yaml`; a stack installed under another account (for example `pgcli`) is not found there. Pass that account's config with `-c`: `pg -c /home/pgcli/.pgcli/pg.yaml addon safeline site cc show --name safeline1 --site-id 4`. `site mode`, `site cc`, `site protect` and `site bot` take the same flag.

> **Community Edition limit: one upstream per site.** The console pgcli installs runs without a Pro license, so a site can proxy to a single upstream. Adding a second upstream — and therefore any load balancing across servers — is refused by the console with `license required`; activate a Pro license in the console to use it.

```bash
pg addon safeline site list --name waf1
pg addon safeline site create --name waf1 \
    --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 \
    --comment "web tier" \
    --cert fullchain.pem --key privkey.pem \
    --health-check --health-check-interval 30 --health-check-path /healthz
pg addon safeline site update --name waf1 --site-id 3 --health-check=false
pg addon safeline site health --name waf1
pg addon safeline site mode --name waf1 --site-id 3 dryrun
pg addon safeline site cc show --name waf1 --site-id 3
pg addon safeline site cc waiting-room --name waf1 --site-id 3 --enabled
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --type req --count 200
pg addon safeline site protect show --name waf1 --site-id 3
pg addon safeline site protect mode balance --name waf1
pg addon safeline site bot show --name waf1 --site-id 3
pg addon safeline site bot enable --name waf1 --site-id 3
pg addon safeline site bot condition --name waf1 --site-id 3 --where 'uri:prefix=/admin'
pg addon safeline site delete --name waf1 --site-id 3

pg addon safeline cert list --name waf1
pg addon safeline cert create --name waf1 --cert fullchain.pem --key privkey.pem
pg addon safeline cert update --name waf1 --cert-id 2 --cert fullchain.pem --key privkey.pem
pg addon safeline cert delete --name waf1 --cert-id 2
```

- `--port` values are the host entry ports; suffix a port with `_ssl` to terminate TLS on it. `--cert`/`--key` upload a PEM certificate (leaf or full chain) and bind it in one step, `--cert-id` binds a certificate already stored in the console, and without either the console's default certificate is used. `--domain` defaults to `*` (any Host header).
- `--upstream` takes absolute `http://` or `https://` URLs; `--balance` is `roundrobin` (default), `leastconn`, `iphash` or `backup`. `site update` replaces the whole list with `--upstream`, or changes it incrementally with `--add-upstream` / `--remove-upstream`. The bundled console runs as the Community Edition, which permits **one upstream per site** — a second one is refused with `license required`; load balancing across several upstreams needs a Pro license.
- `--health-check` probes every upstream over HTTP every 10–3600 seconds at the given path. `site update` changes only the settings you pass (upstreams, health check and/or the bound certificate), leaving everything else at its console values.
- `site health` probes the selected sites (all of them by default) and prints `healthy` or the console's error per upstream; it exits non-zero when any upstream is unhealthy, so it works as a monitoring gate. `site list` shows the site ids that `update`, `delete`, `health` and `mode` take via `--site-id`.
- `site mode` switches one or more sites between the three run modes, described under "Site run modes" below.
- `site cc` reads and changes the two CC-protection settings (the waiting room and the rate-limit rules), described under "CC protection" below.
- `site protect` reads and changes the semantic-analysis modules behind attack protection (攻击防护), described under "Semantic analysis (attack protection)" below.
- `site bot` reads and changes a site's BOT protection (人机验证), described under "BOT protection" below. The Community Edition runs the invisible verification only, and its match conditions skip the Pro-only `geo_*`/`asn_*` operators.
- `cert list` shows stored certificates with their covered domains, expiry and the sites using them. The console refuses to delete a certificate while a site's TLS entry port uses it, so delete or repoint the site first.

A plain HTTP site needs no certificate — just an entry port and an upstream:

```bash
pg addon safeline site create --name waf1 \
    --port 80 --domain www.example.com \
    --upstream http://10.0.0.5:8080
```

tengine binds port 80 directly on the host, so it must be free (see `ss -tlnp`).

To change an existing site's upstreams — replacing the list with a single upstream works on any edition, while adding/removing servers and multi-upstream lists need a Pro license:

```bash
# add a server, keeping the current ones (Pro)
pg addon safeline site update --name waf1 --site-id 3 --add-upstream http://10.0.0.7:8080

# drop a server (Pro, once more than one is configured)
pg addon safeline site update --name waf1 --site-id 3 --remove-upstream http://10.0.0.5:8080

# replace the whole list: one URL works on any edition, several need Pro
pg addon safeline site update --name waf1 --site-id 3 \
    --upstream http://10.0.0.6:8080 --upstream http://10.0.0.7:8080
```

Binding a certificate to a TLS site — either upload it once and bind by id, or upload and bind in one step:

```bash
# upload once, reuse by id (the id is printed and shown by `cert list`)
pg addon safeline cert create --name waf1 --cert fullchain.pem --key privkey.pem
pg addon safeline site create --name waf1 --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 --cert-id 2

# upload and bind in one step (the certificate is rolled back if site creation fails)
pg addon safeline site create --name waf1 --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 --cert fullchain.pem --key privkey.pem
```

Renewing is two different operations, depending on what changed:

```bash
# same certificate renewed (expiry approaching): replace the PEM in place under
# the existing id — every site bound to it serves the new certificate without
# being touched and without a reload
pg addon safeline cert update --name waf1 --cert-id 2 --cert new-fullchain.pem --key new-privkey.pem

# the site should serve a different certificate altogether: store the new one
# (or reuse an id from `cert list`) and rebind the site
pg addon safeline cert create --name waf1 --cert other-fullchain.pem --key other-privkey.pem  # -> id 5
pg addon safeline site update --name waf1 --site-id 3 --cert-id 5
```

Custom error pages and upstream weights/backup roles stay in the console UI.

### Site run modes

Every site runs in one of three modes, mirroring the console's Defense / Audited /
Offline modes. `pg addon safeline site list` shows the current mode in its `MODE`
column, and `pg addon safeline site mode` switches it:

| Mode | Console name | Behavior |
|---|---|---|
| `defense` | Defense mode | the default; detected attacks are blocked and recorded |
| `dryrun` | Audited mode | detections are only recorded, nothing is blocked |
| `offline` | Offline mode | a maintenance page is served; everyone is blocked |

The mode is the only positional argument and the site ids come from `--site-id`
(repeatable or comma-separated), so one call can switch several sites. The change
is published to tengine immediately, exactly like the console's own mode switch.

```bash
# see the current mode (MODE column)
pg addon safeline site list --name waf1

# switch one site to audit mode
pg addon safeline site mode --name waf1 --site-id 3 dryrun

# switch several sites back to defense in one call
pg addon safeline site mode --name waf1 --site-id 3 --site-id 4 defense
```

### CC protection (waiting room and rate limits)

`pg addon safeline site cc` reads and changes the two CC protection (CC 防护)
settings the console shows for a site. They apply while the site runs in
defense mode.

```bash
# both settings at a glance: waiting room, rule scope and effective rules
pg addon safeline site cc show --name waf1 --site-id 3
```

The output shows the waiting room, whether the site's rate limits are its own
or the global set it follows, and the effective rules:

```text
Site 3 (www.example.com) CC protection
  Waiting room:  enabled (max concurrent 5, session timeout 3m)
  Rate limits:   global (3 rules)
TYPE    ENABLED  PERIOD  COUNT  ACTION        BLOCK  CONDITIONS
req     yes      10s     100    challenge_v1  60m    -
attack  no       60s     10     block         30m    -
error   no       10s     10     block         30m    status_code eq 403,404
```

The waiting room limits concurrent visitors — view, enable and disable it:

```bash
pg addon safeline site cc waiting-room --name waf1 --site-id 3
pg addon safeline site cc waiting-room --name waf1 --site-id 3 --enabled
pg addon safeline site cc waiting-room --name waf1 --site-id 3 --enabled=false
```

Its thresholds (`--max-concurrent` 1–5000, `--session-timeout` 1–30 minutes) are
a Pro feature: the Community Edition console applies only `--enabled` and keeps
its defaults (5 concurrent, 3 minutes). The command re-reads the room after
writing and reports any threshold the console did not apply, so a silently
ignored change is not mistaken for success.

> **Not suitable for production.** The waiting room is a browser-facing tool: its queue page needs JavaScript and cookies, so API and CLI clients that do not follow the queue stay in it indefinitely; abandoned sessions hold their slots until the session timeout; and a handful of concurrent requests from curl-style scripts occupy every slot for that long, locking ordinary visitors out — a cheap denial-of-service vector. The Community Edition's fixed 5 concurrent visitors are unusable for real traffic regardless. Protect production with the rate-limit rules below instead — prefer `--action block` for endpoints that non-browser clients call.

Every site has three rate-limit rules — `req` (requests per window), `attack`
(attack hits per window) and `error` (responses matching the rule's
conditions) — and by default follows the global rule set:

```bash
# view the effective rules, optionally one type only
pg addon safeline site cc rate-limit --name waf1 --site-id 3
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --type req

# edit a rule (window 10-600 seconds, count >= 1, block 1-1440 minutes,
# action block / challenge_v1 / challenge_v2)
pg addon safeline site cc rate-limit --name waf1 --site-id 3 \
    --type req --enabled --period 10 --count 200 --action block --block-min 30

# edit the shared rules every follower sees (site 0 in the console)
pg addon safeline site cc rate-limit --name waf1 --global --type attack --count 20

# give one site its own copy (seeded from the globals, all rules disabled),
# then switch it back
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --use-global=false
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --use-global
```

A site that follows the global rules cannot edit them per site: the command
says so and points at `--global` (edit the shared rules) and
`--use-global=false` (make a site-specific copy first). In the copy every rule
starts disabled; combine `--use-global=false` with `--type` and rule flags to
enable and configure one in the same call.

A common hardening, in one call — give the site its own rules (seeded from the
global set) and allow 200 requests per 10 seconds, blocking offenders for 30
minutes:

```bash
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --use-global=false \
    --type req --enabled --period 10 --count 200 --action block --block-min 30
```

### BOT protection (人机验证)

`pg addon safeline site bot` reads and changes the BOT protection the console
lists next to CC protection. It challenges the selected visitors with an
**invisible verification** (无感知验证): the request receives a JavaScript
challenge that browsers solve without user interaction, while clients that
cannot run JavaScript fail it. Every site has its own switch and its own match
conditions; the challenge applies while the site runs in defense mode.

```bash
# whether it is on, the verification level, validity and match conditions
pg addon safeline site bot show --name waf1 --site-id 3

# turn it on / off
pg addon safeline site bot enable --name waf1 --site-id 3
pg addon safeline site bot disable --name waf1 --site-id 3

# challenge only the matching visitors, or everyone again
pg addon safeline site bot condition --name waf1 --site-id 3 \
  --where 'uri:prefix=/admin && method:eq=POST'
pg addon safeline site bot condition --name waf1 --site-id 3 --all-visitors
```

`show` prints the switch, the verification level, how long a passed
verification stays valid and which visitors are challenged:

```text
Site 3 (www.example.com) BOT protection (人机验证)
  Status:    enabled
  Level:     invisible verification
  Validity:  1h
  Match:     only visitors matching the conditions below
GROUP  FIELD   OP      VALUE
1      uri     prefix  /admin
1      method  eq      POST
```

A site that never configured BOT protection reports the console defaults
(disabled, invisible level, one hour, no conditions). `enable` keeps the
stored validity and match conditions; a site without any conditions challenges
every visitor. `condition` fills in the console's "仅针对特定条件启用人机验证"
option in its "满足条件时认证" mode (the other mode, 满足条件时跳过认证, is not
implemented). **Every `bot condition` call replaces the whole condition set**:
the rules passed become exactly the stored conditions — existing rules are
neither kept nor merged, so repeat the ones you still want. `--all-visitors`
clears them all, so every visit is challenged again.

Each `--where` rule is one condition group, groups are ORed, `&&` joins
conditions that must all match (ANDed) and `|` separates the values of one
condition; a condition is `field:operator=value`:

```bash
pg addon safeline site bot condition --name waf1 --site-id 3 \
  --where 'uri:prefix=/admin && method:eq=POST' \
  --where 'src_ip:cidr=10.0.0.0/8|192.168.0.0/16'
```

A typical configuration challenges script clients, requests without a
User-Agent and the admin path, and leaves everything else untouched:

```bash
pg addon safeline site bot enable --name waf1 --site-id 3 \
  --where 'req_header/User-Agent:has=curl' \
  --where 'req_header/User-Agent:has=python-requests' \
  --where 'req_header/User-Agent:has=wget' \
  --where 'req_header/User-Agent:not_exist' \
  --where 'uri:prefix=/admin'
```

Matching clients receive the invisible-verification page with HTTP status 468:
browsers run its JavaScript and pass without interaction, staying verified for
the validity window, while clients that cannot execute JavaScript stay
challenged.

The match targets (the `field` in `field:operator=value`) and the operators
each takes:

| Match target | Spelling | Operators | Notes |
|---|---|---|---|
| Source IP | `src_ip` | `eq` `not_eq` `match` `cidr` `not_cidr` `in` `not_in` `geo_eq` `geo_not_eq` `asn_eq` `asn_not_eq` | e.g. `src_ip:cidr=10.0.0.0/8`; `geo_*`/`asn_*` need Pro and `in`/`not_in` select IP groups — the CLI refuses all of these, see below |
| URI | `uri` | `eq` `not_eq` `has` `not_has` `prefix` `re` | includes the query string; e.g. `uri:prefix=/admin` |
| URI without query | `uri_no_query` | `eq` `not_eq` `has` `not_has` `prefix` `re` | e.g. `uri_no_query:eq=/api/users` |
| Host | `host` | `eq` `not_eq` `match` `re` `has` `not_has` | e.g. `host:eq=www.example.com` |
| Request header | `req_header/<header>` | `eq` `not_eq` `has` `not_has` `re` `exist` `not_exist` | the header name is required; e.g. `req_header/User-Agent:has=curl` |
| Raw request headers | `req_raw_header` | `re` `has` `not_has` | e.g. `req_raw_header:has=python-requests` |
| Raw response headers | `res_raw_header` | `re` `has` `not_has` | e.g. `res_raw_header:has=Set-Cookie` |
| Request body | `req_body` | `re` `has` `not_has` | e.g. `req_body:has=<script` |
| Response body | `res_body` | `re` `has` `not_has` | e.g. `res_body:has=stack trace` |
| GET parameter | `get_param/<param>` | `eq` `not_eq` `has` `not_has` `re` `exist` `not_exist` | the parameter name is required; e.g. `get_param/q:exist` |
| POST parameter | `post_param/<param>` | `eq` `not_eq` `has` `not_has` `re` `exist` `not_exist` | the parameter name is required; e.g. `post_param/username:eq=admin` |
| Method | `method` | `eq` `not_eq` | e.g. `method:eq=POST` |
| Website | `website` | `eq` `not_eq` | value is the site id; e.g. `website:eq=3` |
| JA4 fingerprint | `ja4` | `ja4_in` `eq` `not_eq` | `ja4_in` selects recorded fingerprints; e.g. `ja4:eq=t13d1516h2_8daaf6152771_02713d6af862` |

`exist`/`not_exist` take no value (the `=` may be omitted). Some of the
console's choices are out of reach: `geo_*` and `asn_*` on `src_ip` need a Pro
license, and `in`/`not_in` on `src_ip` plus `ja4_in` select console-managed
items (IP groups, recorded JA4 fingerprints) — configure those in the console.

The console only accepts condition changes while the protection is on, so
`condition` refuses a disabled site and points at `enable --where ...`, which
enables and sets the conditions in one call. The slide verification (滑动验证),
editing the validity and replay protection (请求防重放) are Pro features: the
Community Edition keeps the invisible level and a one-hour validity, and those
are the only values the command writes. Changing anything re-reads the site
afterwards and reports a configuration the console did not apply. Disabling
keeps the stored settings, so enabling again restores them.

### Semantic analysis (attack protection)

`pg addon safeline site protect` reads and changes the semantic-analysis
modules behind the console's attack protection (攻击防护). Fourteen detection
modules — SQL injection, XSS, file upload, command injection, Java/PHP
deserialization, SSTI, SSRF and others — each run at one of four levels:

| Level | Console name | Behavior |
|---|---|---|
| `disable` | 禁用 | do not detect |
| `observe` | 仅观察 | log risky requests, block nothing (`dry_run` on the wire) |
| `balance` | 平衡防护 | block obvious attacks, keep false positives low (the console default) |
| `strict` | 高强度防护 | block more requests, with a higher false-positive rate |

Every site runs the global levels until it gets its own copy — the same
follow-or-override shape as the CC rate limits.

```bash
# global levels, which sites without their own copy run
pg addon safeline site protect show --name waf1

# one site's effective levels, saying whether they are global or custom
pg addon safeline site protect show --name waf1 --site-id 3

# set all 14 modules at once, globally
pg addon safeline site protect mode balance --name waf1

# give one site its own copy at a stricter level
pg addon safeline site protect mode strict --name waf1 --site-id 3

# drop the copy and follow the global levels again
pg addon safeline site protect use-global --name waf1 --site-id 3
```

```text
Global attack protection (semantic analysis): all 14 modules at balance
MODULE                LEVEL
m_sqli                balance
m_xss                 balance
m_file_upload         balance
...
```

The batch leaves the modules the console page does not expose untouched: the
scanner detector (`m_scanner`), response detection (`m_response`) and the
enhanced-rules module (`m_rule`). Individual module levels stay in the console
UI. Changes publish to the detection engine immediately, and they apply while
the site runs in defense mode.

### Threat-intelligence sharing (威胁情报共享计划)

The console's 通用设置 → 威胁情报共享计划 carries three switches that report
what this instance detects back to Chaitin's threat-intelligence servers:

| Switch | Console label | Shares |
|---|---|---|
| `malicious-ips` | IP 情报共享计划 (共享恶意 IP 信息) | malicious IPs detected by this instance |
| `threat-intel` | 共享威胁情报 | attack IPs and key attack logs (desensitized and encrypted) |
| `fingerprint` | 浏览器指纹信息共享计划 (共享浏览器指纹信息) | browser fingerprints |

`pg addon safeline telemetry` reads and switches them:

```bash
# the three switches
pg addon safeline telemetry show --name waf1

# report nothing — the usual choice for private deployments
pg addon safeline telemetry disable --name waf1

# turn every switch back on
pg addon safeline telemetry enable --name waf1
```

```text
Threat-intelligence sharing on "waf1":
SWITCH         STATE     CONSOLE SETTING
malicious-ips  disabled  IP 情报共享计划（共享恶意 IP 信息）
threat-intel   disabled  共享威胁情报
fingerprint    disabled  浏览器指纹信息共享计划
```

The switches only control outbound sharing and the malicious-IP intelligence
groups: the built-in detection modules keep working with all of them off. The
commands compare against the console and only write the switches that differ.

## Container layout and networking

| Role | Network | Address | Purpose |
|---|---|---|---|
| `postgres` | bridge `pgcli-safeline-<name>` | `<subnet>.2` | Bundled PostgreSQL 15.18 |
| `mgt` | bridge | `<subnet>.4` | Console (container port `1443`, published on `--port`) |
| `detector` | bridge | `<subnet>.5` | Detection engine |
| `luigi` | bridge | `<subnet>.7` | Console support service |
| `fvm` | bridge | `<subnet>.8` | File/version service |
| `chaos` | bridge | `<subnet>.10` | Traffic simulation service |
| `tengine` | host | — | Traffic proxy; site ports configured in the console |

The bridge network is created with `--subnet <prefix>.0/24 --gateway <prefix>.1` and owned by the instance (label `pgcli.safeline=<name>`); an existing network with another subnet or owner is an error. Containers use `--restart unless-stopped` (so `pg addon stop` sticks) and a 30-second stop timeout; bridge containers drop `NET_RAW` and set `no-new-privileges`, tengine sets `no-new-privileges` and raises its open-file ulimit as upstream does.

## Data layout

Everything lives under `--data-dir` (`<base_dir>/addon/safeline/<name>/` by default):

```text
<data-dir>/
├── resources/
│   ├── postgres/data   # bundled PostgreSQL data
│   ├── postgres/password  # bundled DB password (0600), reused on re-install
│   ├── mgt/            # console state
│   ├── detector/ nginx/ chaos/ cache/ luigi/
│   └── sock/ run/      # unix sockets shared between containers
└── logs/
    ├── nginx/          # console + tengine access/error logs
    └── detector/
```

The containers run as root inside the container, so these directories are root-owned. Treat the tree as opaque and back it up as a whole if you need to preserve console configuration. PostgreSQL bakes its password into the cluster on first init, so pgcli records it in `resources/postgres/password`; that is what lets `pg addon remove` (without `--clean-data`) followed by a re-install reattach to the surviving data.

## Backup and restore

Logical backups talk to the stack's bundled database directly (the password file is
root-only). Example (instance `safeline1`, default data dir):

```bash
pw=$(cat /data/pg/addon/safeline/safeline1/resources/postgres/password)

# backup / restore (.sql plain SQL, .dump custom, add .gz to compress; --clean drops existing objects)
pg export --dsn "postgres://safeline-ce:$pw@<subnet-prefix>.2:5432/safeline-ce?sslmode=disable" -o safeline.sql
pg import --dsn "postgres://safeline-ce:$pw@<subnet-prefix>.2:5432/safeline-ce?sslmode=disable" -i safeline.sql
```

Replace `<subnet-prefix>` with the instance's `--subnet-prefix` (`172.22.222` by
default); the trailing `.2` is the bundled database's fixed address inside the stack's
/24.

Cold copy: stop the stack, copy the whole `<data-dir>` (console state included), put it
back at the same path and run `pg addon start`.

See [Data Import/Export](/docs/import-export/) for the full `pg export` / `pg import`
options (compression levels, stdin/stdout piping, and more).

## Lifecycle, logs and autostart

```bash
pg addon list                                   # security add-on section: n/7 running, console URL
pg addon start safeline --name waf1             # start the stack (creates missing containers)
pg addon stop safeline --name waf1              # stop in reverse order
pg addon logs safeline --name waf1              # mgt console log (default service)
pg addon logs safeline --name waf1 --service tengine -f   # traffic proxy log
pg addon reload safeline --name waf1            # nginx -t + graceful tengine reload (re-resolves site upstream DNS)
```

`--service` accepts `postgres`, `mgt`, `detector`, `tengine`, `luigi`, `fvm` or `chaos`; the default is `mgt`. `start` waits for PostgreSQL and the console health endpoint — and for tengine's control socket when tengine (re)starts — before returning.

Autostart starts the whole stack on boot in dependency order, and recovers the console password if it was never captured:

```bash
pg autostart enable --safeline --name waf1
```

The protected sites and their traffic ports are configured in the console or with `pg addon safeline site`, not through autostart.

## Uninstall

```bash
pg addon remove safeline --name waf1             # remove containers and the network, keep data
pg addon remove safeline --name waf1 --clean-data   # also delete <data-dir>
```

The dedicated bridge network is removed once no containers are attached. Without `--clean-data` the data directory (bundled PostgreSQL data and console state) is kept so a re-install can resume.

## Troubleshooting

- **“SafeLine requires rootful podman” / “SafeLine requires Linux”** — the stack needs static container addresses and host-network port binding. Run podman as root on Linux; rootless and macOS/Windows are refused up front.
- **“network … already exists with subnet …”** — another stack or a stale network owns that subnet or name. Install with a different `--subnet-prefix`/`--name`, or remove the stale network first.
- **Console does not become healthy** — check `pg addon logs safeline --name <name> --service mgt` and `--service postgres`. The first boot runs schema migrations and can take a minute.
- **Site ports busy** — tengine binds protected sites directly on the host; free the port or pick another entry port in the console.
- **Password lost** — run `pg addon password safeline --name <name> --reset`; it rotates the console password, saves it to the stored `admin_password` and prints the new one (the stack must be running).
- **Images** — the SafeLine CE images are published for `linux/amd64` only; the stack runs on amd64 hosts.

## Limitations

- **Linux + rootful podman only** (static IPs; host-network traffic proxy).
- **One upstream per site (Community Edition).** The bundled console runs without a Pro license, so a site proxies to a single upstream: `--add-upstream`, a multi-URL `--upstream`, weighted/backup upstreams and load balancing are refused with `license required`. Activate a Pro license in the console to use them.
- **BOT protection covers the invisible verification only.** The slide verification, editing the validity and replay protection are Pro features; the Community Edition keeps the invisible level and a one-hour validity. On `src_ip`, the `geo_*`/`asn_*` operators need Pro, and `in`/`not_in` plus `ja4_in` address console-managed items — `site bot condition` rejects all of these. Identity authentication (身份认证) and dynamic protection (动态防护) are not managed by the CLI — configure them in the console.
- Only the console port is published by pgcli; per-site traffic ports are bound by tengine and created through the console or `pg addon safeline site`.
- The console uses SafeLine's own self-signed certificate; no `--tls` integration.
- pgcli generates and manages the internal detector management token; the other optional upstream secrets (`MGT_INTERNAL_TOKEN`, `DETECTOR_AES_KEY`, `MGT_LICENSE_INTEGRITY_KEY`, `RIVER_*`) are left at their upstream defaults.
- SafeLine keeps its state in the bundled PostgreSQL inside `--data-dir`; pgcli's PITR/backup add-ons target pgcli instances and do not back this database up.
