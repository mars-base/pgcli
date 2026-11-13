---
title: "Offline and air-gapped installs"
description: "Serve container images, the pg binary, the podman launcher and runtime downloads (host binaries and DEBs) from a LAN HTTP server"
weight: 15
---

The playbooks can install and operate pgcli without reaching GitHub, ghcr.io,
Docker Hub or distro mirrors. A single static HTTP directory on the LAN can
serve every artifact: consumers always fetch `<base>/<basename>`, so one flat
server root is enough.

## What the server hosts

| Variable | Artifact | Consumed by |
|----------|----------|-------------|
| `pgcli_images_http_url` | `podman save` tarballs produced by `make container-images-export` | `pgcli` role during install; `pg_addon` / `pg_ha_haproxy` lazily |
| `pgcli_install_binary_url` | `pg` binary — raw file or `pgcli_<version>_linux_amd64.tar.gz` | `pgcli` role |
| `pgcli_podman_binary_url` | `podman-launcher` binary | `pgcli` role when `/usr/local/bin/podman` is missing |
| `pgcli_download_mirror` | host binaries (juicefs, zerofs, duckdb, Litestream) and DocumentDB DEBs | `pg` at runtime through `PGCLI_DOWNLOAD_MIRROR` |

All four are set in `group_vars/all/main.yml`; the committed template is
`group_vars/all/main.yml.example`.

## Changing the variables

The live file is gitignored. Copy the template on first use, then edit it in
place:

```bash
cp group_vars/all/main.yml.example group_vars/all/main.yml
$EDITOR group_vars/all/main.yml
```

Point every family at the same server root, and leave a variable empty to keep
the online path for that artifact:

```yaml
pgcli_install_binary_url: "http://192.0.2.10:18080/pg"
pgcli_podman_binary_url: "http://192.0.2.10:18080/podman-launcher-linux_amd64"
pgcli_download_mirror: "http://192.0.2.10:18080"
pgcli_images_http_url: "http://192.0.2.10:18080"
```

Variable precedence is `host_vars/<host>.yml` >
`group_vars/<group>/main.yml` > `group_vars/all/main.yml`. Override only the
hosts or groups that differ instead of editing `all` when both online and
offline hosts are managed from the same inventory. `pgcli_images` is a complete
replacement list, not a merge, so a scenario group that defines its own list
overrides the one in `all`.

Edits take effect on the next playbook run. The binary is only reinstalled when
`pg` is missing or `pgcli_install_force: true`; image tarballs are only fetched
when the tag is absent from the local podman store (or
`pgcli_images_force_load: true`).

On an air-gapped control node, point `pgcli_install_script_src` at a local
checkout instead of the GitHub raw URL:

```yaml
pgcli_install_script_src: "{{ inventory_dir | dirname }}/scripts/install.sh"
```

The role then copies the script to the target host; only an `http(s)://` value
is downloaded on the host.

## Egress proxy

When hosts reach the internet only through a forward proxy (for example a
Privoxy add-on on a connected machine), set the global `pgcli_proxy_url`
instead of — or alongside — the LAN mirror. Proxy startup, the full variable
set, `NO_PROXY` rules, manual SSH usage and verification are covered in
[Egress proxy](./proxy/).

## Starting the server with `pg http`

To export every image and wire the server up to Ansible step by step, see
[Offline image server](./image-server/). The rest of this page places that
server in the context of all offline artifacts.

`pg http` is pgcli's built-in static file server. It runs as a detached host
process (no container) and supports `HEAD`/`Range` requests, which the image
loader and its pre-checks rely on.

```bash
# On a host with podman and a repository checkout: export the tarballs.
make container-images-export          # writes images/*.tar, skips existing files

# Serve that directory. The default is 127.0.0.1:18080.
pg http start --listen 0.0.0.0 --port 18080 --dir images

pg http status
pg http stop http-18080
```

- If the requested port is busy, the next free port is used (up to 100
  attempts). Check `pg http status` and keep the URLs in `group_vars/all`
  consistent with the port actually bound.
- The server is detached and survives the terminal that started it. It does
  not start at boot unless enabled:
  `pg autostart enable --http --name http-18080`.
- There is no authentication. Keep the listener on a trusted network. `--tls`
  encrypts traffic but does not authenticate clients.
- Files must sit directly in the served directory: every consumer requests
  `<base>/<basename>`, with no path prefix.

## Container images

`make container-images-export` exports every image in the Makefile `IMAGES`
list as `images/<repo>_<tag>.tar` (`:` becomes `_`; the export pins
`linux/amd64`). The manifest in `group_vars/all/main.yml` must use exactly
those archive names:

```yaml
pgcli_images:
  - image: ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0
    archive: pgcli-pg_18-2.58.0.tar
  - image: ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0
    archive: pgcli-backup_2.58.0.tar
  - image: docker.io/edoburu/pgbouncer:v1.25.2-p0
    archive: pgbouncer_v1.25.2-p0.tar
    auto_load: true
    addons: [pgbouncer]
```

How the list is consumed:

- **Entries without `auto_load`** are core images. The install play loads them
  into podman's local store after checking that each archive is present.
- **`auto_load: true` with `addons: [type]`** marks add-on images. They are
  loaded on demand by the `pg_addon` role when an add-on of a matching type is
  first created (`-` in the type name becomes `_`). `pg_ha_haproxy` uses the
  same mechanism for HAProxy.
- Setting `pgcli_images_http_url` makes `scripts/install.sh` run with
  `--skip-pull`; no image is pulled from a registry.
- Before changing anything, the install play sends `HEAD` requests to the
  server root and to every core archive, and aborts with the offending names
  if any is unreachable. Lazy add-on archives are fetched when first needed.

Trim the list to the images a scenario actually uses: the first run on a host
with an empty store transfers only the listed core archives. Keep `pgcli-pg`
and `pgcli-backup`; add one entry per add-on type in use (remove `auto_load`
to preload it during install instead of on first use).

Transferred tarballs are cached on the target host. The cache is kept across
runs — only missing images are transferred again — and is safe to delete:

| Layout | Cache directory |
|--------|-----------------|
| rootful | `/var/cache/pgcli/images` |
| rootless | `~/.cache/pgcli/images` under the deployment account |

Rootless podman uses a separate image store owned by the deployment account.
Images already loaded as root are not visible there, so the rootless install
loads its own copy from the offline server.

## The `pg` binary and the podman launcher

`pgcli_install_binary_url` accepts either a raw `pg` binary or a release
tarball named `pgcli_<version>_linux_amd64.tar.gz`. A tarball is extracted and
its top-level `pg` member is installed to `pgcli_install_path`
(`/usr/local/bin/pg` by default); a raw file is copied as-is. In both cases
`scripts/install.sh` still runs with `--skip-binary`, so host initialization
(uidmap, `/etc/containers/policy.json`, user namespaces, SELinux) is not
skipped.

`pgcli_podman_binary_url` installs the `podman-launcher` binary as
`/usr/local/bin/podman` when no podman exists on the host. The launcher fetches
its static podman tree on first use, so a fully air-gapped host still needs
podman pre-installed (distribution package or a pre-seeded launcher state)
before the first `podman` invocation succeeds.

## Runtime downloads: host binaries and DEBs

Some add-ons download assets when they are created, after installation is
already complete. `pgcli_download_mirror` redirects those downloads: the
playbooks export it as `PGCLI_DOWNLOAD_MIRROR` to every `pg` command, and
pgcli rewrites any `http(s)` asset URL to `<mirror>/<basename>`. URLs already
under the mirror and non-HTTP paths are left untouched.

Place the upstream assets under their original file names:

| Consumer | Asset basename |
|----------|----------------|
| `pg addon install juicefs` | `juicefs-1.4.1-linux-amd64.tar.gz` / `juicefs-1.4.1-linux-arm64.tar.gz` |
| `pg addon install zerofs` | `zerofs-pgo-multiplatform.tar.gz` |
| `pg addon install duckdb` | `duckdb_cli-linux-amd64.zip` (1.x, default 1.5.6) or `duckdb-cli-linux-amd64.tar.gz` (2.x, pinned 2.0.0); amd64 only |
| `pg sqlite install` (Litestream) | `litestream-0.5.17-linux-x86_64.tar.gz` (amd64) / `litestream-0.5.17-linux-arm64.tar.gz` |
| `pg extension install documentdb` | `ubuntu24.04-postgresql-18-documentdb_1.0-0_<arch>.deb` and `libicu74_74.2-1ubuntu3.1_<arch>.deb` |

Behavior and caveats:

- pgcli still verifies pinned SHA-256 digests after downloading, so mirrored
  files must be byte-identical to the upstream release assets.
- Use pinned versions. `--version latest` queries the GitHub API, which is not
  redirected by the mirror.
- The DocumentDB DEBs are copied into the derived extension image and
  installed there; the mirror covers exactly these two packages.
- Other catalog extensions are built from the Pigsty DEB repository inside
  `podman build`, which `pgcli_download_mirror` does not redirect. With
  [`pgcli_proxy_url` set](./proxy/), the build's apt and curl use that proxy;
  on a fully
  air-gapped network, pre-build the derived `-ext` image on a connected host
  (or mirror the Pigsty repository and point apt at it on the build host) and
  distribute the image through `pgcli_images`.
- Litestream is not an add-on: `pg sqlite install` provisions sqlite3 and the
  pinned Litestream binary. Set `pgcli_sqlite_install: true` on the hosts or
  groups that use SQLite and the `pgcli` role runs it during the install play,
  with the same mirror redirect. sqlite3 itself comes from the distribution
  package manager, so air-gapped hosts need that package preinstalled (or a
  local distro mirror).
- Outside Ansible, export `PGCLI_DOWNLOAD_MIRROR` yourself before running
  manual `pg addon install` / `pg extension install` / `pg sqlite install`
  commands.

## Reference file list

The current LAN server is a complete mirror of the default `pgcli_images`
manifest (44 image tarballs) plus the install binaries and every runtime asset
at the versions pgcli pins. Use it as the authoritative checklist when building
a new server; regenerate the image names with `make container-images-export`
whenever image tags change.

Each line is `<sha256>  <filename>` (`sha256sum` format), so a downloaded
directory can be verified with `sha256sum -c`.

```text
# Container images (podman save, docker-archive)
57afcb88cc34c24516b32714599c523cae5e4c92e736467062829833e585257d  alertmanager_v0.34.1.tar
a14e80bef8be07614c0291ce2355ad8d894319c2a6f0cf4c4f1beaa6cf522b44  beegfs-meta_8.4.0.tar
4b4f84fbb63325c91145569562bfd611f8d99f663eb9d1eac2b7ca4a60df310e  beegfs-mgmtd_8.4.0.tar
bee066403b9f0c993d0771ef6a1b043a0dd3493042eec8c4bad283d3de34ff90  beegfs-storage_8.4.0.tar
93d7058def24e8433289f81d7618dd5db6ed1615e1d636f1cc62cc065b7e078d  clickhouse-server_26.8.36.3.tar
3b656d42cdbefb81818dcdd14f72a1c6399d15e3d6a3ea9630fd1b28929b7275  etcd_v3.5.30.tar
5e48fd56e8e09ba45ea65f7a2756329b17031ff9c714ee63b447b3a850ab6e19  garage_v2.4.1.tar
bdbdd7f0c98ebf27f200eb700ed843af342b3cb0fb2160fda1fc501750f2f214  gitea_28.1.0.tar
bdbaf2766541a181981ca09e4303591e27cbc48a40df1b14e4e6e399bba72616  grafana_13.2.4.tar
26d7b2146246600e91acaf82c90cb617cc03f2bfb326931f34da9e498b2e67e0  haproxy_3.2.23-alpine.tar
1227f164fdfd18f0394faa3f0661a01268987e4d83dbd5788e946388cdee4fa4  kafka_4.4.0.tar
0a793f19e64e7f83256bc3f6bf7bdf91e57fd01a1c7a9a3bc6eaea67786116df  nginx_1.27-alpine.tar
36f9aa3df9b4dbb92c04224c642a614c71757a6a51eb9b8b7fdf4336774458e2  node-exporter_v1.12.1.tar
f36fd9dc6da057b14b51c7a913349e8951aed6ab2c14bd786098e3fb851369fd  pgadmin4_9.18.tar
3110efd6d12a2542f1f033367fd00289b86aaf2150ccd8ada8737e846df0ca28  pgbouncer_v1.25.2-p0.tar
c12395bb1c9ea0b8095fbc8ff3806958de28ee44d418247490340a8e74fcfc98  pgcli-backup_2.58.0.tar
2b15818c746bbbce40da49bfff111c69663c822941b2a7f1cbcfec9f267420a1  pgcli-documentdb-gateway_1.0.0.tar
03d80d54bfa21bce01827ae557547f15584f6b906762a21a2d196cddcfe05f94  pgcli-influx2tsdb_1.3.2.tar
a227354e55153e1ad470e05e95000aa177395d8007a9a70a3e8513ff0173bc6e  pgcli-influxdb_1.13.1.tar
d1cd9b9447132d8728211fb5e05830e9c6d536dfe8db7a8eb094e1cdcbb567bb  pgcli-mc_20250813083541.tar
412b8ac7decfebb977ba85b77aaf35d0fe95da63de71f2e10e8639e31c97d96c  pgcli-minio_20250422221226.tar
9179e64e34c72db01dae3748ff4118a93796a9c0bba0826e49d70b6b8720d10a  pgcli-mongosh_2.14.1.tar
0519713fdf5510b72e3bd2b5f69ab4ef73695eeb3d5f6a822383da62754542b0  pgcli-patroni_18-4.1.5.tar
46bbe0a99d342c967097c4147fa0ebd1ce8f6c0128676080f11efeb1be21f831  pgcli-pg_18-2.58.0.tar
377877a78d2fc29a02ed4658946d2f248f909a524bb4fc091bb3d4acdb70d413  pgcli-privoxy_4.2.0.tar
e13cc780306750ad6dd6d543d41249fb8e62dfff01c956a16d2a4f093d49a6b8  pgcli-rustfs_1.0.0.tar
5ab1ad708255e084d60cabbb7dbe807ea1c26dd767ef86502732f5f1132ebca2  pgdog_v0.1.57.tar
529231f4bfc8ea0240ec753a36aea676970ac39606529e219b751d03648926ff  pg_exporter_1.5.0.tar
5cbbce30d39c3640dc64052f220aa3d825f3218f36ef5083a0351a3db9db9989  postgrest_v16.3.tar
e8a81d28d62dd12b2cdf1a75f04cd3107f222d049d89d268e2e887fc2c112bed  predixy_7.0.1-alpine.tar
b4efb1483945d650e09d2c8e3b1970f9acee02d42c5d75517b8e2b2be4812131  prometheus_v3.15.0.tar
857d511abc27aef7103d64af4c2fa9b1425ab45f30f5d1198532f0db166cfaed  rabbitmq_4.4.0-management.tar
1b8591ac18ab53e3cc249357682e0c66c07375e1c58fd8781fa3315946ee8119  redis_7.4.11.tar
27cf00f651008c6c317ab4eb18a5b5e3415b3a5c376b8f723d8ab644dda3cba1  redis_8.10.2.tar
cb595d76e5d748f39715a1a4b76db1eba9fb26ed38c597d9b9b21e193491e1b0  rocketmq_5.5.0.tar
f3744934bd7b2c7b09d0194968c39b44469fd131ffbd6a291f36db009dba773b  seaweedfs_4.48_large_disk.tar
82e865abbc16a57b44b2e78bd70b31b311e6130542ddfdc49cc382c835522985  silo_RELEASE.2026-09-16T00-00-00Z.tar
4cc9904054ac3387538b1d152f51a2ecc78d4f25ff030a940eacfe8c23fcf0c4  victoria-logs_v1.53.0.tar
f39023521577ea6d0ca335de42cd016327a4bd63fbceccdfa58a080324671406  victoria-metrics_v1.154.1.tar
e8dd5799352cea82bdb326448bf2ab6748d3a3d28ca9e5ea3ae47a86f444c9d7  vlagent_v1.53.0.tar
942804cc5bed04499803ec5dcba6933734fec480963c308a64364de6becf30bb  vmalert_v1.133.0.tar
3cf4053637bd7a01e378a54ac08a4a5496cbeaf37b143aaf43f6b32ff97c0374  vminsert_v1.154.1-cluster.tar
934bd154d33533752cce8078106a30cee408e688bca7b3d552bf0efdcb8dbf32  vmselect_v1.154.1-cluster.tar
3d4705f25e9fa100e2acc60a098b180c777bfc1654757dd5025ee655253bfede  vmstorage_v1.154.1-cluster.tar

# Install binaries
262a992266b0b7f18d2e2dbf043d7fb3ebfffab702bae0bfd4c769d9a50deb51  pg
689c841cb5e9f86dec84b095b7cc2581b2af51e7ee3bb3e8067b92487b312b1d  podman-launcher-linux_amd64

# Runtime mirror assets
c6507206baf7793c6013dbd82368469a45f20dc0e17a6f92f204602f652ed2bc  duckdb-cli-linux-amd64.tar.gz
6e89deac1ebbc36eed0291caf8b567b030c7b86ac35998f71854e22b3c5d5e2f  duckdb_cli-linux-amd64.zip
01ee09a21a9351a465e09906f113845e1c6a19bea70f530e5e2b2125b2dd3b82  juicefs-1.4.1-linux-amd64.tar.gz
1015ade83a7a93180a29f6c93ee5780a3eda52522331934ef2ef0cc0921995fd  juicefs-1.4.1-linux-arm64.tar.gz
c9a70989678660eed9a1e904c74fa043da8bec8e2036856fc16e31ced79b04f8  libicu74_74.2-1ubuntu3.1_amd64.deb
48f93acf50dcf237a8d58ce366730a28438ce52d3f06d7a2a88b51261dd791f7  libicu74_74.2-1ubuntu3.1_arm64.deb
f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5  litestream-0.5.17-linux-arm64.tar.gz
cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d  litestream-0.5.17-linux-x86_64.tar.gz
cc26f8225af13233f901f86292d5a8373276654454c671316cd603d20378fdbf  ubuntu24.04-postgresql-18-documentdb_1.0-0_amd64.deb
6afc91e21b72990d1d43421b79e4484d7cd55366c1d0436fa71f4cc2f119d6eb  ubuntu24.04-postgresql-18-documentdb_1.0-0_arm64.deb
367b2e79e05167af8409b28322f21b1ae5de033fc40820a3c27684a9974a56f2  zerofs-pgo-multiplatform.tar.gz
```

The runtime assets cover every create-time download through
`pgcli_download_mirror`, at the versions pgcli pins: juicefs 1.4.1
(amd64/arm64), duckdb 1.5.6 and 2.0.0 (amd64 only), Litestream 0.5.17
(x86_64/arm64), zerofs, and the DocumentDB DEBs. Other versions fail the
pinned SHA-256 check, so update the pin in pgcli before mirroring a new
release.

## Verifying the setup

Check one archive per family, then run a deployment:

```bash
curl -I http://192.0.2.10:18080/pgcli-pg_18-2.58.0.tar
curl -I http://192.0.2.10:18080/pg
curl -I http://192.0.2.10:18080/ubuntu24.04-postgresql-18-documentdb_1.0-0_amd64.deb

uv run ansible-playbook playbooks/apply.yaml --limit single
uv run ansible single -m command -a 'pg list'
```

The install play fails before making changes when the server root or a core
archive is unreachable; a `HEAD` request returning anything other than `200`
is treated as missing.
