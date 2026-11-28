---
title: "Offline deployment"
description: "End-to-end air-gapped runbook: export images and binaries, serve them over LAN HTTP, point group_vars/all at the server, run apply.yaml and verify"
weight: 14
---

This is the runbook for installing pgcli on hosts that cannot reach GitHub,
ghcr.io, Docker Hub or distro mirrors. It chains the asset reference in
[Offline and air-gapped installs](./offline/) and the exporter/serving detail
in [Offline image server](./image-server/) into the order you actually run
them.

Host roles:

| Role | Needs |
|------|-------|
| Staging host (has internet) | `pgcli` repo checkout, Podman, Go and make — runs the exporter |
| Asset server (LAN) | `[http]` inventory group; `pg` already installed; no internet |
| Control node | `pgcli-ansible` checkout with synced `uv` env; reaches the targets |
| Targets | Reach the asset server; Podman installed (no internet) |

## 1. Clone the Ansible repository

On the control node, while it still has internet (clone, `uv sync` and
`hosts.ini` must all be done before the node moves into the isolated network):

```bash
myproj=xx                              # your project name
git clone --depth 1 https://github.com/mars-base/pgcli-ansible.git ansible-${myproj}
cd ansible-${myproj}
uv sync
cp ansible.cfg.example ansible.cfg
cp hosts.ini.example hosts.ini        # then edit for your hosts
```

This checkout is where the inventory, `group_vars` and playbooks below live.

## 2. Export the assets

On the staging host:

```bash
./scripts/export-images.sh --ansible ansible/group_vars/all/main.yml \
  --binaries -o /srv/offline-images
```

`--ansible` names each tarball from `pgcli_images[].archive`, keeping the
playbook's `HEAD` preflight and the server filenames in sync; `--binaries`
adds `pg` and `podman-launcher-linux_<arch>`. Trim `pgcli_images` to the
images you actually need **before** exporting: every remaining archive must
exist on the server, and entries with `auto_load` are fetched on first use of
that add-on rather than at install time. Truly isolated targets need Podman
preinstalled — the launcher only bootstraps a static Podman tree.

Instead of building `pg` from the checkout, both binaries can be downloaded
from GitHub straight into the served directory (Linux amd64 shown):

```bash
# pg: release tarball (the role unpacks the top-level `pg`)
curl -fLo /srv/offline-images/pgcli_2.9.8_linux_amd64.tar.gz \
  https://github.com/mars-base/pgcli/releases/download/v2.9.8/pgcli_2.9.8_linux_amd64.tar.gz

# podman launcher: latest upstream release
curl -fLo /srv/offline-images/podman-launcher-linux_amd64 \
  https://github.com/89luca89/podman-launcher/releases/latest/download/podman-launcher-linux_amd64

chmod 0755 /srv/offline-images/podman-launcher-linux_amd64
```

`pgcli_install_binary_url` accepts the tarball as-is — the role extracts the
top-level `pg`. `pgcli_podman_binary_url` must point at the raw launcher
binary; archives are not unpacked there.

## 3. Deploy the HTTP asset server

Declare the asset server in `hosts.ini` — `deploy-http.yaml` targets the
`[http]` group — then declare the server in `group_vars/http/main.yml` (copy
the `.example` next to it first). It is the download site for the image
tarballs and the two binaries:

```ini
[http]
assets ansible_host=192.0.2.11 ansible_user=root
```

```yaml
pgcli_http_servers:
  - name: images
    dir: /srv/offline-images
    listen: 0.0.0.0
    port: 18080
    autostart: true
```

```bash
uv run ansible-playbook playbooks/deploy-http.yaml --limit http
```

The role installs pgcli on the host, creates the served directory, starts the
server and re-converges on later runs. The role does not copy image tarballs
or binaries, so put the step-2 output into the directory (staging host and
control node may be the same machine):

```bash
scp -r /srv/offline-images/. assets:/srv/offline-images/
```

Files must sit directly in the served directory — every consumer requests
`<base>/<basename>`. The server has no authentication, so keep it on a trusted
network; `HEAD` and `Range` support are required.

The server needs `pg` before it can serve it: on a fresh air-gapped host,
place a `pg` binary at `/usr/local/bin/pg` (extract it from the release
tarball or build one with `--binaries`; alternatively point
`pgcli_install_binary_url` at an asset server that is already running) before
this play.

## 4. Point Ansible at the server

In `group_vars/all/main.yml` (copied from the example on first use):

```yaml
pgcli_install_binary_url: "http://192.0.2.10:18080/pgcli_2.9.8_linux_amd64.tar.gz"
pgcli_podman_binary_url: "http://192.0.2.10:18080/podman-launcher-linux_amd64"
pgcli_download_mirror: "http://192.0.2.10:18080"
pgcli_images_http_url: "http://192.0.2.10:18080"
```

Leaving a variable empty keeps that class of asset on the online path. The
`pgcli_install_binary_url` example matches the step-2 tarball; point it at
`/pg` instead when the served artifact is the raw binary from
`export-images.sh --binaries`. On an
isolated control node also point `pgcli_install_script_src` at the
`scripts/install.sh` of your local `pgcli` checkout (for example
`/srv/pgcli/scripts/install.sh`). With
`pgcli_images_http_url` set, the installer runs with `--skip-pull`.

## 5. Run the playbook

The example inventory ships with only `[http]` active: list the target hosts
in `[all]` and uncomment the scenario group you deploy (for example
`[single]`). Then, in the checkout from step 1:

```bash
uv run ansible-playbook playbooks/ping.yaml --limit single
uv run ansible-playbook playbooks/apply.yaml --limit single
```

`apply.yaml` is idempotent — re-run it after changing inventory or variables.
Use the group that matches your topology (`single`, `samehost`, `crosshost`,
`addon_<type>_<mode>`, `ha_etcd`/`ha_cluster`, …).

## 6. Verify

```bash
# the server answers with 200 and a Content-Length
curl -I http://192.0.2.10:18080/pgcli-pg_18-2.59.3.tar

# the instance is managed
uv run ansible single -m command -a 'pg list'
```

The install output for the first host should show core images loading from
`pgcli_images_http_url` instead of a registry pull. Add-on images marked
`auto_load` download on the first add-on install of that type — confirm the
corresponding tarball is requested then.

## Updating an offline site

New image tags change archive filenames; re-run the exporter and the old
tarballs stay behind (they are not deleted). Existing hosts skip loading when
the tag already exists locally; set `pgcli_images_force_load: true` to force
a reload. If the hosts also sit behind a proxy, the asset server is added to
`NO_PROXY` automatically — see [Egress proxy](./proxy/).
