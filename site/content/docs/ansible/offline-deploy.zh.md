---
title: "离线部署"
description: "端到端隔离部署流程：导出镜像与二进制、局域网 HTTP 提供、配置 group_vars/all、运行 apply.yaml 并验证"
weight: 14
---

本页是把 pgcli 装进**无法访问 GitHub、ghcr.io、Docker Hub 和发行版镜像站**的
主机的操作流程，把[离线与隔离环境安装](./offline/)的资源清单、
[离线镜像服务器](./image-server/)的导出与服务细节，按实际执行顺序串起来。

各主机角色：

| 角色 | 需要 |
|------|------|
| 导出机（可联网） | `pgcli` 主仓库 checkout、Podman、Go 与 make——运行导出脚本 |
| 资产服务器（局域网） | `[http]` inventory 分组；首次运行需能访问 GitHub 以便 play 安装 `pg`（或预先放好二进制）；除此之外不联网 |
| 控制节点 | 已 `uv sync` 的 `pgcli-ansible` checkout；能连到目标主机 |
| 目标主机 | 能访问资产服务器；已安装 Podman（不联网） |

## 1. 克隆 Ansible 仓库

在控制节点仍可联网时完成（clone、`uv sync` 与 `hosts.ini` 都要在进入隔离网络前做完）：

```bash
myproj=xx                              # 你的项目名
git clone --depth 1 https://github.com/mars-base/pgcli-ansible.git ansible-${myproj}
cd ansible-${myproj}
uv sync
cp ansible.cfg.example ansible.cfg
cp hosts.ini.example hosts.ini        # 再按实际主机修改
```

后续的 inventory、`group_vars` 与 playbook 都在这个 checkout 里。

## 2. 导出离线资产

在导出机上：

```bash
./scripts/export-images.sh --ansible ansible/group_vars/all/main.yml \
  --binaries -o /srv/offline-images
```

`--ansible` 按 `pgcli_images[].archive` 命名 tar 包，保证 playbook 的 `HEAD`
预检与服务器文件名一致；`--binaries` 额外产出 `pg` 与
`podman-launcher-linux_<arch>`。导出**之前**先把 `pgcli_images` 裁剪到实际
用到的镜像：清单里留下的每个 archive 都必须在服务器上存在；带 `auto_load`
的条目不随安装加载，而是在首次使用对应 add-on 时按需下载。完全隔离的目标
主机需要预装 Podman——launcher 只负责引导静态 Podman 树。

也可以不从本地编译 `pg`，直接从 GitHub 把两个二进制下载到被服务目录（以 Linux amd64 为例）：

```bash
# pg：发布压缩包（角色会解出顶层的 `pg`）
curl -fLo /srv/offline-images/pgcli_2.9.8_linux_amd64.tar.gz \
  https://github.com/mars-base/pgcli/releases/download/v2.9.8/pgcli_2.9.8_linux_amd64.tar.gz

# podman launcher：上游最新发布
curl -fLo /srv/offline-images/podman-launcher-linux_amd64 \
  https://github.com/89luca89/podman-launcher/releases/latest/download/podman-launcher-linux_amd64

chmod 0755 /srv/offline-images/podman-launcher-linux_amd64
```

`pgcli_install_binary_url` 可以直接指向该压缩包——角色会解出顶层的 `pg`。
`pgcli_podman_binary_url` 必须指向裸的 launcher 二进制；该 URL 不会解压压缩包。

## 3. 部署 HTTP 资产服务器

先在 `hosts.ini` 里声明资产服务器（`deploy-http.yaml` 作用于 `[http]` 组），
再在 `group_vars/http/main.yml`（从同目录 `.example` 复制）里声明服务器——
它就是离线镜像 tar 包和两个二进制的 HTTP 下载站点：

```ini
[http]
assets ansible_host=192.0.2.10 ansible_user=root
```

```yaml
pgcli_http_servers:
  - name: images
    dir: /srv/offline-images
    listen: 0.0.0.0
    port: 18080
    autostart: true
```

`deploy-http.yaml` 会自行安装 `pg`：`pgcli` role 按 `pgcli_install_binary_url`
下载二进制并以 `--skip-binary --skip-pull` 运行仓库自带的安装脚本；若
`pgcli_install_path` 上已有 `pg`，这整套流程直接跳过。因此该 URL 在 play 运行时
必须可达——首次部署请指向一台已在运行的资产服务器（全新服务器在 `pg` 就位前无法
提供自己的二进制）；或者事先把 `pg` 放到主机上，role 就会跳过下载。两者都没有时，
仓库自带的安装脚本会从 GitHub 下载 `pg`，该主机需要能出网访问 github.com（以及
提供 `curl`、`tar` 的发行版仓库）。

```bash
uv run ansible-playbook playbooks/deploy-http.yaml --limit http
```

角色会在这台主机上安装 pgcli、创建被服务目录、启动服务器，并在后续运行中持续
收敛。镜像 tar 包与二进制都不由角色搬运，把步骤 2 的导出结果放进该目录即可
（导出机与控制节点可以是同一台）：

```bash
scp -r /srv/offline-images/. assets:/srv/offline-images/
```

文件必须直接放在被服务目录下——所有消费者都按 `<base>/<basename>` 请求。
服务器没有认证，只应监听在可信网络；`HEAD` 与 `Range` 支持是必需的。

## 4. 让 Ansible 指向服务器

首次使用时从模板复制 `group_vars/all/main.yml`，再填入：

```yaml
pgcli_install_binary_url: "http://192.0.2.10:18080/pgcli_2.9.8_linux_amd64.tar.gz"
pgcli_podman_binary_url: "http://192.0.2.10:18080/podman-launcher-linux_amd64"
pgcli_download_mirror: "http://192.0.2.10:18080"
pgcli_images_http_url: "http://192.0.2.10:18080"
```

变量留空表示该类产物继续走在线路径。示例的 `pgcli_install_binary_url` 对应
步骤 2 的压缩包；若服务器上放的是裸二进制（`export-images.sh --binaries`），
把地址改成 `/pg`。隔离的控制节点还要把
`pgcli_install_script_src` 指向本地 `pgcli` checkout 里的 `scripts/install.sh`
（例如 `/srv/pgcli/scripts/install.sh`）。设置了
`pgcli_images_http_url` 后，安装过程带 `--skip-pull` 运行。

## 5. 运行剧本

示例 inventory 默认只启用 `[http]`：把目标主机列进 `[all]`，并取消注释要部署的
场景分组（例如 `[single]`）。然后在步骤 1 的 checkout 里：

```bash
uv run ansible-playbook playbooks/ping.yaml --limit single
uv run ansible-playbook playbooks/apply.yaml --limit single
```

`apply.yaml` 幂等——修改 inventory 或变量后重跑即可。按实际拓扑换成对应分组
（`single`、`samehost`、`crosshost`、`addon_<type>_<mode>`、`ha_etcd`/`ha_cluster` 等）。

## 6. 验证

```bash
# 服务器可达，且返回 200 与 Content-Length
curl -I http://192.0.2.10:18080/pgcli-pg_18-2.59.3.tar

# 实例已被管理
uv run ansible single -m command -a 'pg list'
```

第一台主机安装时，核心镜像应显示从 `pgcli_images_http_url` 加载，而不是从
registry 拉取。带 `auto_load` 的插件镜像在首次安装该类型 add-on 时才下载——
届时确认请求了对应的 tar 包。

## 更新离线站点

镜像 tag 变更会改变 archive 文件名；重跑导出脚本即可，旧 tar 包不会自动删除。
主机本地已有对应 tag 时会跳过加载；需要强制重新加载时设
`pgcli_images_force_load: true`。主机同时有出网代理时，资产服务器会自动加入
`NO_PROXY`——见[出网代理](./proxy/)。
