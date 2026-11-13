---
title: "Offline image server"
description: "Export every container image on a connected host and serve the tarballs over HTTP for air-gapped installs driven by pgcli_images"
weight: 17
---

An air-gapped install needs two things: a directory of image tarballs reachable on
the LAN, and `pgcli_images_http_url` pointing at it. The repository ships
`scripts/export-images.sh` for the first part: it reads the list from
`docs/images.md` or from the Ansible `pgcli_images` variable, pulls each image,
and saves it as a `docker-archive` tarball whose name matches the offline
manifest. `pg http` provides the second part.

```text
connected host                              isolated network
export-images.sh ──▶ /srv/offline-images ──HTTP──▶ pgcli_images_http_url
  (podman pull/save)      (pg http)               (loaded into podman)
```

## Export the images

By default the script reads the image tables in `docs/images.md` and writes to
`images/` under the repository root:

```bash
./scripts/export-images.sh
```

**Using the Ansible list as the source is recommended**: then each tarball name
comes from `pgcli_images[].archive`, so it cannot drift from what the playbooks
consume:

```bash
# Use the live group_vars/all/main.yml when present, otherwise main.yml.example
./scripts/export-images.sh --ansible ansible/group_vars/all/main.yml -o /srv/offline-images
```

Both sources resolve to 44 images with the same image-to-filename mapping.
Options:

| Option | Meaning |
|--------|---------|
| `--manifest FILE` | Use another Markdown image list (mutually exclusive with `--ansible`) |
| `--ansible FILE` | Read `pgcli_images` from an Ansible group_vars file and use its `archive` names |
| `-o, --output DIR` | Output directory; default `images/` in the repository |
| `--platform ARCH` | Pull platform; default `linux/amd64` (use `linux/arm64` for ARM hosts) |
| `-n, --dry-run` | Print the plan without pulling or writing anything |
| `-f, --force` | Re-export tarballs that already exist |

Check the plan first:

```bash
./scripts/export-images.sh --ansible ansible/group_vars/all/main.yml --dry-run
```

On a host that needs a proxy, the script inherits the environment and `podman
pull` uses the same egress:

```bash
export HTTP_PROXY=http://192.0.2.10:8118
export HTTPS_PROXY=$HTTP_PROXY
./scripts/export-images.sh -o /srv/offline-images
```

See [Egress proxy](./proxy/) for running the proxy itself.

### Naming and behaviour

- Names follow `<repository-basename>_<tag>.tar` (`:` becomes `_`), for example
  `ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0` → `pgcli-pg_18-2.58.0.tar`.
  With `--ansible`, the `archive` field is used verbatim.
- Existing tarballs without `--force` are reported as `SKIP`, so an interrupted
  run can simply be restarted.
- Each archive is written to `.tmp` and atomically renamed on success; a failure
  never leaves a partial file. One failed image does not stop the rest; the
  script exits non-zero and lists the failures at the end.
- Images are pulled with `--platform` before saving because `podman save`
  cannot write a multi-arch manifest list to `docker-archive`.
- The 44 tarballs total several GB; make sure the output filesystem has room.

## Start the HTTP server

`pg http` is pgcli's built-in static file server. It runs as a detached host
process (no container) and supports `HEAD`/`Range` requests, which image
pre-checks and loading rely on.

```bash
# Serve the export directory; the default listener is 127.0.0.1:18080
pg http start --name images --listen 0.0.0.0 --port 18080 --dir /srv/offline-images

# Confirm the actual address and port (it moves to the next free port if busy)
pg http status
```

Enable it at boot if needed:

```bash
pg autostart enable --http --name images
```

Caveats:

- **There is no authentication.** Use `0.0.0.0` only on a trusted network or
  behind a firewall. `--tls` encrypts traffic but does not authenticate clients,
  and its self-signed certificate must be trusted by the clients.
- If the port is busy, `pg http` tries the next free one (up to 100 attempts).
  Always check `pg http status` and keep `pgcli_images_http_url` consistent with
  the port actually bound.
- Files must sit directly in the served directory: consumers request
  `<base>/<basename>` with no path prefix.

## Configure Ansible

Point the cluster at the server root in `group_vars/all/main.yml`:

```yaml
pgcli_images_http_url: "http://192.0.2.10:18080"
```

With it set, `scripts/install.sh` runs with `--skip-pull` and the install phase
never contacts a registry. Two requirements follow:

1. Every `archive` in `pgcli_images` must match a filename on the server
   exactly. Exporting with `--ansible` guarantees this.
2. All core images (entries without `auto_load`) must be present. Before
   changing anything, the install play sends `HEAD` requests to the server root
   and to every core archive; any miss aborts with the specific filename.

Images marked `auto_load: true` are fetched on demand: the first time an add-on
of that type is created. You can therefore export only the add-on images you
actually use. Manifest trimming and lazy loading are covered in
[Offline and air-gapped installs](./offline/).

If `pgcli_proxy_url` is set as well, the image server hostname is added to
`NO_PROXY` automatically, so LAN downloads never go through the proxy.

## Verify

On one target host in the isolated network:

```bash
# The server answers with 200 and a Content-Length
curl -I http://192.0.2.10:18080/pgcli-pg_18-2.58.0.tar

# Run the playbook
cd ansible
uv run ansible-playbook playbooks/apply.yaml
```

The first host should report loading the core images from
`pgcli_images_http_url` rather than pulling from a registry. When an add-on is
created for the first time, confirm that its tarball is downloaded on demand.

## Updating images

When tags change, re-export from the new `pgcli_images` list; filenames change
with them, and old tarballs are not deleted automatically:

```bash
./scripts/export-images.sh --ansible ansible/group_vars/all/main.yml -o /srv/offline-images
```

Existing files are skipped; add `--force` when a tarball must be pulled again.
The directory is flat, so removing stale tarballs that the manifest no longer
references does not affect images already in use.
