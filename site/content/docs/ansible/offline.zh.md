---
title: "离线与隔离环境安装"
description: "用局域网 HTTP 服务器提供容器镜像、pg 二进制、podman launcher 以及运行时下载资源（主机二进制与 DEB 包）"
weight: 15
---

整套 playbook 可以在不访问 GitHub、ghcr.io、Docker Hub 和发行版镜像站的情况下完成安装与运行。局域网上一台静态 HTTP 服务器即可承载全部产物：所有消费者都按 `<base>/<basename>` 取文件，因此一个平铺的服务器根目录就够了。

## 服务器承载的内容

| 变量 | 产物 | 使用方 |
|------|------|--------|
| `pgcli_images_http_url` | `make container-images-export` 生成的 `podman save` tar 包 | 安装阶段的 `pgcli` 角色；`pg_addon` / `pg_ha_haproxy` 懒加载 |
| `pgcli_install_binary_url` | `pg` 二进制——裸文件或 `pgcli_<version>_linux_amd64.tar.gz` | `pgcli` 角色 |
| `pgcli_podman_binary_url` | `podman-launcher` 二进制 | 主机上不存在 `/usr/local/bin/podman` 时的 `pgcli` 角色 |
| `pgcli_download_mirror` | 主机二进制（juicefs、zerofs、duckdb、Litestream）与 DocumentDB DEB | `pg` 运行时，通过 `PGCLI_DOWNLOAD_MIRROR` |

四个变量都在 `group_vars/all/main.yml` 中设置；入库模板是 `group_vars/all/main.yml.example`。

## 如何修改变量

实际文件被 gitignore 忽略。首次使用时从模板复制，然后直接编辑：

```bash
cp group_vars/all/main.yml.example group_vars/all/main.yml
$EDITOR group_vars/all/main.yml
```

把四类来源都指向同一个服务器根目录；某个变量留空表示该类产物继续走在线路径：

```yaml
pgcli_install_binary_url: "http://192.0.2.10:18080/pg"
pgcli_podman_binary_url: "http://192.0.2.10:18080/podman-launcher-linux_amd64"
pgcli_download_mirror: "http://192.0.2.10:18080"
pgcli_images_http_url: "http://192.0.2.10:18080"
```

变量优先级为 `host_vars/<host>.yml` > `group_vars/<group>/main.yml` > `group_vars/all/main.yml`。同一份 inventory 里既有在线主机又有离线主机时，只覆盖有差异的主机或分组，不要全部写进 `all`。`pgcli_images` 是**整体替换**而不是合并，因此某个场景分组定义了自己的列表时会覆盖 `all` 中的列表。

修改在下一次运行 playbook 时生效。`pg` 只在缺失或 `pgcli_install_force: true` 时重装；镜像 tar 包只在本地 podman 存储里没有对应 tag（或 `pgcli_images_force_load: true`）时才会重新下载。

在隔离的控制节点上，把 `pgcli_install_script_src` 指向本地 checkout，而不是 GitHub raw URL：

```yaml
pgcli_install_script_src: "{{ inventory_dir | dirname }}/scripts/install.sh"
```

此时角色把脚本复制到目标主机；只有 `http(s)://` 开头的值才会在目标主机上下载。

## 出网代理

主机只能通过正向代理访问公网时（例如在一台可联网机器上运行 Privoxy add-on），
设置全局 `pgcli_proxy_url` 即可，可与局域网镜像同时使用。代理启动、完整变量、
`NO_PROXY` 规则、手动 SSH 用法与验证方法见[出网代理](./proxy/)。

## 用 `pg http` 启动服务器

`pg http` 是 pgcli 自带的静态文件服务器。它以脱离终端的主机进程运行（不占容器），支持 `HEAD`/`Range` 请求——镜像加载与预检正好依赖这两个能力。

```bash
# 在装有 podman 和仓库代码的主机上导出 tar 包
make container-images-export          # 写入 images/*.tar，已存在的文件会跳过

# 提供该目录。默认监听 127.0.0.1:18080
pg http start --listen 0.0.0.0 --port 18080 --dir images

pg http status
pg http stop http-18080
```

- 端口被占用时会自动尝试下一个空闲端口（最多 100 次）。用 `pg http status` 确认实际端口，并保证 `group_vars/all` 中的 URL 与之一致。
- 服务器是脱离终端的后台进程，关闭终端不受影响。默认不开机自启；需要时执行 `pg autostart enable --http --name http-18080`。
- **没有认证**。监听地址应限制在可信网络内。`--tls` 只加密流量，不认证客户端。
- 文件必须直接位于被服务目录下：所有消费者都请求 `<base>/<basename>`，不带路径前缀。

## 容器镜像

`make container-images-export` 会把 Makefile `IMAGES` 列表中的镜像导出为 `images/<repo>_<tag>.tar`（`:` 变成 `_`；导出固定为 `linux/amd64`）。`group_vars/all/main.yml` 里的清单必须使用完全一致的 archive 名称：

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

清单的消费方式：

- **不带 `auto_load` 的条目**是核心镜像。安装 play 在确认 archive 存在后把它们加载进 podman 本地存储。
- **`auto_load: true` + `addons: [type]`** 标记为插件镜像。首次创建对应类型的 add-on 时，由 `pg_addon` 角色按需加载（类型名中的 `-` 写成 `_`）。`pg_ha_haproxy` 对 HAProxy 使用同一机制。
- 设置 `pgcli_images_http_url` 后，`scripts/install.sh` 会带 `--skip-pull` 运行，不会从任何 registry 拉取镜像。
- 改动任何东西之前，安装 play 会向服务器根目录和每个核心 archive 发送 `HEAD` 请求；任一不可达就带着具体文件名中止。插件镜像的 archive 在首次需要时才下载。

按场景裁剪清单：在镜像存储为空的主机上，第一次运行只会传输清单里的核心 archive。`pgcli-pg` 和 `pgcli-backup` 必须保留；用到的插件各留一条（去掉 `auto_load` 改为安装阶段预加载，或保留它改为首次使用时加载）。

下载过的 tar 包会缓存在目标主机上，缓存跨多次运行保留（只重新传输缺失的镜像），可以随时删除：

| 形态 | 缓存目录 |
|------|----------|
| rootful | `/var/cache/pgcli/images` |
| rootless | 部署账号家目录下的 `~/.cache/pgcli/images` |

rootless podman 使用部署账号独立的镜像存储，root 已加载的镜像在那里不可见，因此 rootless 安装会从离线服务器加载自己的一份副本。

## `pg` 二进制与 podman launcher

`pgcli_install_binary_url` 支持裸 `pg` 二进制，也支持名为 `pgcli_<version>_linux_amd64.tar.gz` 的发布压缩包。压缩包会先解压，再把顶层的 `pg` 成员安装到 `pgcli_install_path`（默认 `/usr/local/bin/pg`）；裸文件则直接复制。两种情况下 `scripts/install.sh` 仍会带 `--skip-binary` 运行，因此主机初始化（uidmap、`/etc/containers/policy.json`、user namespace、SELinux）不会被跳过。

`pgcli_podman_binary_url` 在主机没有 podman 时把 `podman-launcher` 安装为 `/usr/local/bin/podman`。launcher 会在首次使用时下载自己的静态 podman 树，因此完全隔离的主机仍需预先安装 podman（发行版软件包或预置 launcher 状态），否则第一次执行 `podman` 会失败。

## 运行时下载：主机二进制与 DEB 包

部分 add-on 是在创建时才下载资源的，此时安装阶段早已结束。`pgcli_download_mirror` 用来重定向这些下载：playbook 会把它导出为 `PGCLI_DOWNLOAD_MIRROR` 传给每条 `pg` 命令，pgcli 把任意 `http(s)` 资源 URL 改写为 `<mirror>/<basename>`；已经在 mirror 下的 URL 和非 HTTP 路径保持不变。

把上游资源按原始文件名放进服务器根目录：

| 使用方 | 资源 basename |
|--------|---------------|
| `pg addon install juicefs` | `juicefs-1.4.1-linux-amd64.tar.gz` / `juicefs-1.4.1-linux-arm64.tar.gz` |
| `pg addon install zerofs` | `zerofs-pgo-multiplatform.tar.gz` |
| `pg addon install duckdb` | `duckdb_cli-linux-amd64.zip`（1.x，默认 1.5.6）或 `duckdb-cli-linux-amd64.tar.gz`（2.x，固定 2.0.0）；仅 amd64 |
| `pg sqlite install`（Litestream） | `litestream-0.5.17-linux-x86_64.tar.gz`（amd64）/`litestream-0.5.17-linux-arm64.tar.gz` |
| `pg extension install documentdb` | `ubuntu24.04-postgresql-18-documentdb_1.0-0_<arch>.deb` 与 `libicu74_74.2-1ubuntu3.1_<arch>.deb` |

行为与注意事项：

- 下载完成后 pgcli 仍会校验固定的 SHA-256，因此 mirror 上的文件必须与上游发布资源逐字节一致。
- 使用固定版本。`--version latest` 会访问 GitHub API，这条请求不会被 mirror 重定向。
- DocumentDB 的两个 DEB 会被复制进派生扩展镜像并在其中安装；mirror 覆盖的正是这两个包。
- 其他扩展目录中的扩展在 `podman build` 阶段从 Pigsty DEB 仓库安装，`pgcli_download_mirror` 不会重定向这一步。设置 [`pgcli_proxy_url`](./proxy/) 后，构建中的 apt 和 curl 会走该代理；完全隔离的网络上仍应先在联网主机上构建好派生的 `-ext` 镜像（或镜像 Pigsty 仓库并在构建主机上配置 apt），再通过 `pgcli_images` 分发该镜像。
- Litestream 不是 add-on：`pg sqlite install` 负责安装 sqlite3 与固定版本的
  Litestream。在用到 SQLite 的主机或分组上设置 `pgcli_sqlite_install: true`，
  `pgcli` 角色会在安装 play 中执行该命令，下载同样走 mirror。sqlite3 本身来自
  发行版包管理器，隔离主机需预装该包（或提供本地发行版镜像源）。
- 在 Ansible 之外手动执行 `pg addon install` / `pg extension install` /
  `pg sqlite install` 时，需要自行导出 `PGCLI_DOWNLOAD_MIRROR`。

## 参考文件清单

当前局域网服务器是默认 `pgcli_images` 清单的完整镜像（44 个镜像 tar 包），另含安装二进制，以及 pgcli 固定版本对应的全部运行时 mirror 资源。搭建新服务器时可以把它当作权威清单；镜像 tag 变化后，用 `make container-images-export` 重新生成文件名。

每行格式为 `<sha256>  <文件名>`（`sha256sum` 格式），下载后可直接用 `sha256sum -c` 校验。

```text
# 容器镜像（podman save，docker-archive 格式）
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

# 安装二进制
262a992266b0b7f18d2e2dbf043d7fb3ebfffab702bae0bfd4c769d9a50deb51  pg
689c841cb5e9f86dec84b095b7cc2581b2af51e7ee3bb3e8067b92487b312b1d  podman-launcher-linux_amd64

# 运行时 mirror 资源
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

运行时资源已覆盖所有在创建时通过 `pgcli_download_mirror` 下载的资源，版本与 pgcli 固定的版本一致：juicefs 1.4.1（amd64/arm64）、duckdb 1.5.6 与 2.0.0（仅 amd64）、Litestream 0.5.17（x86_64/arm64）、zerofs 与 DocumentDB DEB。其他版本无法通过固定 SHA-256 校验，需要先在 pgcli 中更新固定值再镜像新版本。

## 验证配置

每类产物各检查一个，然后跑一次部署：

```bash
curl -I http://192.0.2.10:18080/pgcli-pg_18-2.58.0.tar
curl -I http://192.0.2.10:18080/pg
curl -I http://192.0.2.10:18080/ubuntu24.04-postgresql-18-documentdb_1.0-0_amd64.deb

uv run ansible-playbook playbooks/apply.yaml --limit single
uv run ansible single -m command -a 'pg list'
```

服务器根目录或任一核心 archive 不可达时，安装 play 会在做任何修改之前失败；`HEAD` 请求返回非 `200` 即视为缺失。
