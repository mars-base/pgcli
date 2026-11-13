---
title: "离线镜像服务器"
description: "在联网主机导出全部容器镜像，用 pg http 提供静态 HTTP 服务，供隔离机房按 pgcli_images 清单加载"
weight: 17
---

隔离机房的安装只需要两件事：一份放在局域网上的镜像 tar 包目录，以及指向它的
`pgcli_images_http_url`。仓库里的 `scripts/export-images.sh` 负责第一步：从
`docs/images.md` 或 Ansible 的 `pgcli_images` 变量读取清单，逐个拉取并导出为
`docker-archive` tar 包，文件名与离线清单完全一致。`pg http` 负责第二步。

```text
联网主机                                   隔离机房
export-images.sh ──▶ /srv/offline-images ──HTTP──▶ pgcli_images_http_url
  （podman pull/save）      （pg http）              （load 进 podman）
```

## 导出镜像

脚本默认读取 `docs/images.md` 的镜像表，导出到仓库下的 `images/`：

```bash
./scripts/export-images.sh
```

**推荐用 Ansible 清单作为来源**，这样 tar 文件名直接取自 `pgcli_images[].archive`，
不会和 playbook 消费的文件名出现偏差：

```bash
# 有实际的 group_vars/all/main.yml 时用它，否则用 main.yml.example
./scripts/export-images.sh --ansible ansible/group_vars/all/main.yml -o /srv/offline-images
```

两种来源都解析出 44 个镜像，且镜像到文件名的映射一致。常用参数：

| 参数 | 说明 |
|------|------|
| `--manifest FILE` | 改用其它 Markdown 镜像表（与 `--ansible` 互斥） |
| `--ansible FILE` | 从 Ansible group_vars 的 `pgcli_images` 读取，使用其中的 `archive` 命名 |
| `-o, --output DIR` | 保存目录，默认仓库下的 `images/` |
| `--platform ARCH` | 拉取架构，默认 `linux/amd64`；ARM 主机用 `linux/arm64` |
| `-n, --dry-run` | 只打印计划，不拉取、不写文件 |
| `-f, --force` | 重新导出已存在的 tar 包 |

导出前建议先确认清单：

```bash
./scripts/export-images.sh --ansible ansible/group_vars/all/main.yml --dry-run
```

在需要代理出网的主机上，脚本继承环境里的代理变量，`podman pull` 会走同一出口：

```bash
export HTTP_PROXY=http://192.0.2.10:8118
export HTTPS_PROXY=$HTTP_PROXY
./scripts/export-images.sh -o /srv/offline-images
```

代理本身的部署见[出网代理](./proxy/)。

### 文件命名与行为

- 文件名规则为 `<镜像仓库名>_<tag>.tar`（`:` 替换为 `_`），例如
  `ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0` → `pgcli-pg_18-2.58.0.tar`。
  使用 `--ansible` 时完全以清单中的 `archive` 为准。
- 已存在且未加 `--force` 的 tar 包会 `SKIP`，中断后可以直接重跑续传。
- 每个包先写成 `.tmp`，成功后原子改名，失败不会留下半个文件；单个镜像失败会
  继续处理其余镜像，最后以非零状态退出并列出失败项。
- 先按 `--platform` 拉取再 `podman save`，因为 `podman save` 无法把多架构
  manifest list 写成 `docker-archive`。
- 全部 44 个 tar 包总计数 GB，请确保保存目录所在文件系统有足够空间。

## 启动 HTTP 服务器

`pg http` 是 pgcli 自带的静态文件服务器，以脱离终端的主机进程运行（不占容器），
支持 `HEAD` 和 `Range` 请求——镜像预检和加载正依赖这两个能力。

```bash
# 提供导出目录；默认只监听 127.0.0.1:18080
pg http start --name images --listen 0.0.0.0 --port 18080 --dir /srv/offline-images

# 确认实际监听地址和端口（端口被占用时会自动顺延）
pg http status
```

需要开机自启时：

```bash
pg autostart enable --http --name images
```

注意事项：

- **没有认证**。`0.0.0.0` 只应在可信网段使用，或由防火墙限制访问来源。`--tls`
  只加密流量，不认证客户端；自签证书还需客户端信任。
- 端口被占用时 `pg http` 会自动尝试后续端口（最多 100 次）。务必用
  `pg http status` 核对实际端口，并让 `pgcli_images_http_url` 与之保持一致。
- 文件必须直接位于被服务目录下：所有消费者请求的是 `<base>/<basename>`，不带
  任何路径前缀。

## 配置 Ansible

在 `group_vars/all/main.yml` 中指向服务器根目录：

```yaml
pgcli_images_http_url: "http://192.0.2.10:18080"
```

设置后，`scripts/install.sh` 会带 `--skip-pull` 运行，安装阶段不再访问任何
registry。此时有两项要求：

1. `pgcli_images` 中每条 `archive` 必须与服务器上的文件名完全一致；用
   `--ansible` 模式导出即可自动保证。
2. 核心镜像（不带 `auto_load` 的条目）必须全部存在；安装 play 会先向服务器和
   每个核心 archive 发送 `HEAD` 请求，任一不可达就中止并报出文件名。

带 `auto_load: true` 的插件镜像按需下载：首次创建对应类型的 add-on 时才拉取并
加载，因此可以只导出实际用到的插件镜像。清单裁剪与懒加载规则见
[离线与隔离环境安装](./offline/)。

如果同时设置了 `pgcli_proxy_url`，服务器主机名会自动加入 `NO_PROXY`，局域网
下载不会绕到代理上。

## 验证

在隔离机房的一台目标主机上：

```bash
# 服务器可达，且返回 200 与 Content-Length
curl -I http://192.0.2.10:18080/pgcli-pg_18-2.58.0.tar

# 运行 playbook
cd ansible
uv run ansible-playbook playbooks/apply.yaml
```

检查第一台主机的安装输出：核心镜像应显示从 `pgcli_images_http_url` 加载，而不是
从 registry 拉取。之后首次创建 add-on 时，再确认对应插件 tar 被按需下载。

## 更新镜像

镜像 tag 变更后，以 `pgcli_images` 的新清单为准重新导出；文件名随之变化，旧的
tar 包不会自动删除：

```bash
./scripts/export-images.sh --ansible ansible/group_vars/all/main.yml -o /srv/offline-images
```

同名文件已存在时会跳过；确认需要重新拉取某个包时加 `--force`。服务器目录是
平铺结构，删除不再被清单引用的旧 tar 包不会影响正在使用的镜像。
