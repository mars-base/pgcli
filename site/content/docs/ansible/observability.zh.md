---
title: "机房可观测部署"
description: "用一个声明式 Ansible 场景管理大量机器：node 与 PostgreSQL exporter、Prometheus、告警规则、Alertmanager 和 pg daemon webhook 接收端"
weight: 35
---

`observable` 场景把一批机器变成可观测的机房：所有成员安装 `node_exporter`，
PostgreSQL 主机额外安装 `pg_exporter`，其中一台运行 Prometheus、Alertmanager、
Grafana 和告警 webhook。实例、插件和 webhook 进程全部在 group 变量中声明，由一次
`apply.yaml` 收敛。增加机器只需要增加 inventory 条目并重跑剧本，不需要逐台执行命令。

随仓库提供的参考文件是 `group_vars/observable/main.yml.example`；
`group_vars/observable/README.md` 包含完整的 Grafana API 操作步骤。

## 参考布局

| 主机 | 角色 |
|------|------|
| `db-01` | 单机实例 `pg01`、`nodeexporter`、`pgexporter` |
| `db-02` | 主库 `pg02`、`nodeexporter`、`pgexporter` |
| `db-03` | 跨机只读副本 `pg_primary_ro1`、`nodeexporter`、`pgexporter` |
| `obs-01` | `nodeexporter`、`prometheus`、`alertmanager`、`grafana`、`alertmanager-webhook` daemon |

机器更多时布局不变：把所有机器列进 `[observable]`，把 PostgreSQL 实例加进
`pgcli_instances`，抓取目标会从 inventory 自动推导。以下命令都在 `ansible/` 目录
下执行。

## 大量机器的 inventory

```ini
[all]
db-01 ansible_host=192.0.2.11 private_ip=192.0.2.11
db-02 ansible_host=192.0.2.12 private_ip=192.0.2.12
db-03 ansible_host=192.0.2.13 private_ip=192.0.2.13
db-04 ansible_host=192.0.2.15 private_ip=192.0.2.15
obs-01 ansible_host=192.0.2.14 private_ip=192.0.2.14

[all:vars]
ansible_user=root
ansible_port=22

# 所有被监控机器都属于这个组；node_exporter 会装到每台机器上，
# 可观测主机本身也包含在内。
[observable]
db-01
db-02
db-03
db-04
obs-01

[observable:vars]
ansible_group_priority=20

# 可选的子分组，用于分批发布和环境标签。它们不会替代 [observable]：
# 这些主机仍然属于 observable，group_vars/observable 照常生效。
[rack-a]
db-01
db-02
[rack-b]
db-03
db-04
obs-01
```

`private_ip` 是节点之间抓取和复制使用的地址，`ansible_host` 只用于 SSH 连接。
两个地址分开可以让监控流量留在私网内。

### 自定义分组名称

`observable` 只是随仓库提供的示例名称。实际机房可以按相同结构复制组变量文件，
文件名与 inventory 分组同名：

```bash
cp group_vars/observable/main.yml.example group_vars/site-a/main.yml
cp group_vars/observable/main.yml.example group_vars/site-b/main.yml
```

然后在 `hosts.ini` 中声明同名分组：

```ini
[site-a]
db-01
db-02
obs-01

[site-b]
db-10
db-11
obs-02

[site-a:vars]
ansible_group_priority=20
[site-b:vars]
ansible_group_priority=20
```

用 `--limit site-a` 或 `--limit site-b` 运行。组变量按主机成员关系生效，每个机房
拥有自己的 `pgcli_instances`、`pgcli_addons`，需要时还有 `pgcli_daemons`；不要让
同一台主机同时属于两个场景分组，因为这些列表不会合并，最终由 Ansible 的组优先级
决定哪一个生效。场景分组不要以 `addon_` 开头，该前缀保留给
`addon_<type>_<single|cluster>` 选择器。复制示例后，记得把文件里的 `host:` 值改成
新分组中的主机名，把 `_observable_node_exporter_targets` 里的
`groups['observable']` 改成新的分组名，并在 `group_vars/<分组>/secrets.yaml` 中
创建 `pgexporter_monitor_password`（gitignored，见下文）。

## 抓取目标与标签

示例从 inventory 推导两个抓取 job：

```yaml
# 运行 node_exporter 的主机：[observable] 的所有成员。
_observable_node_exporter_targets: >-
  {{ groups['observable'] | map('extract', hostvars, 'private_ip')
     | map('regex_replace', '$', ':9100') | join(',') }}

# 运行 pg_exporter 的主机：每个承载 pgcli 实例的主机一台。
_observable_pg_exporter_targets: >-
  {{ pgcli_instances | map(attribute='host') | unique
     | map('extract', hostvars, 'private_ip')
     | map('regex_replace', '$', ':9630') | join(',') }}
```

两者都以 `--scrape <job>=<targets>` 传给 Prometheus，并在每次 apply 时重新生成。
因此新增到 `[observable]` 的机器（或加入 `pgcli_instances` 的实例）会自动进入
抓取列表。这是静态抓取配置，不是服务发现：Prometheus 拿到的是明确的
`host:port`。

要附加环境、机架等标签，在目标列表之后追加 `KEY=VALUE`。该 flag 按标签集接受
多个 `--scrape`；按组生成一组 spec 的写法如下：

```yaml
_observable_node_scrapes: >-
  {{ (['--scrape'] | product([
       'node-exporter=' ~ (groups['rack-a'] | map('extract', hostvars, 'private_ip')
                            | map('regex_replace', '$', ':9100') | join(',')) ~ ' rack=rack-a',
       'node-exporter=' ~ (groups['rack-b'] | map('extract', hostvars, 'private_ip')
                            | map('regex_replace', '$', ':9100') | join(',')) ~ ' rack=rack-b'
     ])) | list | flatten }}
```

然后把 `_observable_node_scrapes` 放进插件的 `params`，替换原来的单个
`--scrape` 条目。告警规则里可以通过 `$labels.rack` 使用这些标签。

`nodeexporter` 的插件条目没有 `host` 键，所以会安装到 play 中的每台主机。只监控、
不跑 PostgreSQL 的机器也可以放进 `[observable]`：它只贡献 node_exporter 目标，
不会产生 pg 目标。

## Group 变量与密钥

```bash
cp group_vars/observable/main.yml.example group_vars/observable/main.yml
```

场景设置 `pgcli_deploy_rootless: true` 和 `pgcli_deploy_user: pgcli`，因此 pgcli、
podman、容器和 webhook daemon 都以非特权的 `pgcli` 账号运行，所有派生路径跟随
`pgcli_deploy_home`。

整个机群的实例在这个组的 `pgcli_instances` 列表中声明一次（name、host，以及可选的
副本设置）。每台主机只收敛 `host` 与自身匹配的条目。单机差异优先写到
`host_vars/<host>.yml`，不要复制整个组文件。

所有 PostgreSQL exporter 共用一个低权限监控角色。主库和单机实例上由 `pgexporter`
创建角色（`pg_monitor`、`CONNECTION LIMIT 5`），密码固定取自
`group_vars/observable/secrets.yaml`（gitignored，绝不提交）：

```yaml
pgexporter_monitor_password: "<choose a strong value>"
```

物理 standby 不能执行创建角色的 SQL。流复制会把主库创建的角色带到 standby，
standby 的 exporter 用同一个密码，通过 `--dsn` 连接本地端口。保持默认角色名
`pgexporter`；改名后需要在主库重新创建角色并再复制一次。

## 分批部署

```bash
cd ansible
uv sync
uv run ansible-playbook playbooks/apply.yaml --limit observable
```

实例 play 和插件 play 都是 `serial: 1`，大批主机会逐台部署。多机架上线时按窗口
选择子分组；`--limit rack-a` 仍然会加载 `group_vars/observable`，因为组变量的
生效取决于主机成员关系，而不是 limit：

```bash
uv run ansible-playbook playbooks/apply.yaml --limit rack-a
# 确认 Prometheus targets 后再继续：
uv run ansible-playbook playbooks/apply.yaml --limit rack-b
```

按 tag 的部分运行：

```bash
uv run ansible-playbook playbooks/apply.yaml --limit observable --tags instance  # 实例
uv run ansible-playbook playbooks/apply.yaml --limit observable --tags addon     # exporter + 监控栈
uv run ansible-playbook playbooks/apply.yaml --limit observable --tags daemon    # webhook 接收端
```

剧本顺序是安装 → 实例 → daemon → 插件。每个角色都会对比 `pg config show` 和容器
状态，无变化的主机报告 `ok`。Prometheus 和 Alertmanager 条目上的 `force: true`
会重建容器，使变更后的参数（`--scrape`、`--webhook-url`、规则文件）生效；没有它，
已安装的插件会保留原来的 flag。这里刻意重建 Prometheus，因为它的抓取目标由
inventory 推导，机群变化时必须更新。

## Prometheus 容量

整个机群由单个 Prometheus 插件抓取，需要按规模规划：

- 默认 `--retention 15d`；需要更长的本地历史时调大（例如 `30d`），或把
  `--data-dir` 指向大盘。
- 默认 `--scrape-interval 15s`；机群很大时可改成 `30s` 或 `60s`，样本量按比例
  下降。
- 本地 TSDB 是单实例，插件不支持集群。需要更长保留期或跨机房汇总时，增加
  `--remote-write-url` 指向 VictoriaMetrics 插件，并缩短本地保留期。远端故障
  恢复后写队列会重放。
- Prometheus 没有认证。保持示例中的 `--listen=127.0.0.1` 由 Grafana 代理查询，
  或用防火墙限制端口。

## 告警规则与路由

Prometheus 通过 Prometheus 条目上的 `rule_files` 映射加载
`misc/prometheus/postgres.rules.yml`。`pg_addon` 角色把文件复制到 pgcli 的受管
规则目录，并在安装时传入 `--rule-file`。规则内容放在真实文件中，因此 Prometheus
自己的 `{{ $labels.* }}` 模板不会被 Ansible 二次渲染。规则覆盖 PostgreSQL
（`pg_up`、复制延迟、replay 暂停、连接数饱和、长事务、死锁、归档失败）、node
目标（抓取失败、磁盘空间、只读文件系统）以及 Alertmanager 配置重载。

触发的告警发给 Alertmanager，它的唯一 receiver 是由 `pg daemon` 托管的 webhook：

| 项目 | 值 |
|------|-----|
| 脚本 | `misc/webhook/alertmanager_webhook.py`（复制到 daemon 目录） |
| 命令 | `/usr/bin/python3 <daemon 目录>/alertmanager_webhook.py --listen 127.0.0.1:18801` |
| 日志 | `<daemon 目录>/logs/alertmanager-webhook.log` |
| 轮转 | 10 MB，保留 7 份 |
| 开机自启 | 已启用 |

接收端对每条告警打印一行 JSON，并关闭 HTTP 访问日志，因此 daemon 日志中只有告警
消息。在可观测主机上以部署账号查看：

```bash
runuser -u pgcli -- /usr/local/bin/pg -c /home/pgcli/.pgcli/pg.yaml \
  daemon status alertmanager-webhook
runuser -u pgcli -- /usr/local/bin/pg -c /home/pgcli/.pgcli/pg.yaml \
  daemon logs alertmanager-webhook -n 50
runuser -u pgcli -- /usr/local/bin/pg -c /home/pgcli/.pgcli/pg.yaml \
  autostart status
```

`pg autostart status` 显示 `daemon alertmanager-webhook enabled`；开机时
`pg start --autostart` 先启动实例，再启动插件，最后启动 daemon。

渲染出的 Alertmanager 配置只有一条路由和一个 receiver。需要按 severity 或团队
分流时，给插件条目提供完整的 `conf`（角色以 0600 写入并作为 `--conf-file` 传入；
密钥只放在 gitignored 的正式文件中）：

```yaml
  - type: alertmanager
    name: alertmanager
    host: obs-01
    state: present
    force: true
    params:
      - "--listen=127.0.0.1"
    conf: |
      route:
        receiver: default
        group_by: [alertname, env]
        routes:
          - matchers: ['severity="critical"']
            receiver: oncall
      receivers:
        - name: default
          webhook_configs:
            - url: http://127.0.0.1:18801/alertmanager
        - name: oncall
          webhook_configs:
            - url: http://127.0.0.1:18801/alertmanager
```

内置接收端只接受 `POST /alertmanager` 和 `GET /health`；要区分 receiver 名称，
需扩展 `deliver()`，或把 `oncall` 指向外部 API。Alertmanager 插件运行单实例并
关闭 cluster gossip，因此不是 HA —— 保留它的数据目录（silence 在容器重建后仍然
存在），主机故障时重新启动插件即可。

要把告警转发到第三方 API，在 `misc/webhook/alertmanager_webhook.py` 的
`deliver()` 中实现 HTTP 调用。返回 `False` 会应答 503，让 Alertmanager 重试。
令牌不要进仓库：从部署账号拥有的 0600 文件中读取。Alertmanager 会按组发送通知，
目标 API 非幂等时用 `alert["fingerprint"]` 去重。重跑 `--tags daemon` 会在脚本
内容变化时重写文件并重启 daemon。

## 验证

在可观测主机上执行 loopback 探测：

```bash
curl -sS http://127.0.0.1:9090/api/v1/targets \
  | jq -r '.data.activeTargets[] | "\(.labels.job)\t\(.labels.instance)\t\(.health)"'
curl -sS http://127.0.0.1:9090/api/v1/targets \
  | jq '[.data.activeTargets[] | select(.health != "up")] | length'
curl -sS http://127.0.0.1:9090/api/v1/rules \
  | jq -r '.data.groups[].rules[] | "\(.name)\t\(.health)"'
curl -sf -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9093/-/ready
curl -sS http://127.0.0.1:18801/health
```

每个 target 都应为 `up`（非 `up` 数量为 `0`），每条规则应为 `ok`，Alertmanager
返回 `200`，webhook 返回 `{"status":"ok"}`。通过 Alertmanager 注入一条测试告警，
默认 `group_wait`（约 30 秒）后出现在 daemon 日志中：

```bash
curl -sS -X POST http://127.0.0.1:9093/api/v2/alerts \
  -H 'Content-Type: application/json' \
  -d '[{"labels":{"alertname":"WebhookTest","severity":"warning"},
        "annotations":{"summary":"test"}}]'
```

用过去的 `endsAt` 再发一次相同告警即可解除它。

## Grafana

Grafana 的 admin 密码由 pgcli 生成；取密码时不要写入 Ansible 日志：

```bash
GF_PASSWORD=$(ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml \
  -e HOSTS=obs-01 | sed -n 's/^ *Login password: //p')
GRAFANA=http://obs-01:3000
```

创建 Prometheus 数据源（两个容器都使用 host network，所以 URL 是 loopback），
然后导入两个大盘：

```bash
curl -sS -u "admin:$GF_PASSWORD" \
  -H 'Content-Type: application/json' \
  -X POST "$GRAFANA/api/datasources" \
  --data '{
    "name": "Prometheus",
    "type": "prometheus",
    "url": "http://127.0.0.1:9090",
    "access": "proxy",
    "isDefault": true,
    "editable": true,
    "jsonData": {"httpMethod": "POST"}
  }'

curl -sS -o /tmp/node-exporter-full.json \
  https://grafana.com/api/dashboards/1860/revisions/latest/download
jq --arg ds Prometheus '
  .templating.list |= map(
    if .name == "ds_prometheus"
    then .current = {selected: true, text: $ds, value: $ds}
    else . end)
  | {dashboard: ., overwrite: true, folderId: 0}
' /tmp/node-exporter-full.json > /tmp/node-exporter-import.json
curl -sS -u "admin:$GF_PASSWORD" -H 'Content-Type: application/json' \
  -X POST "$GRAFANA/api/dashboards/db" --data @/tmp/node-exporter-import.json

jq -n --slurpfile d group_vars/observable/postgresql-pgexporter.json \
  '{dashboard: $d[0], overwrite: true, folderId: 0}' > /tmp/pg-dashboard-import.json
curl -sS -u "admin:$GF_PASSWORD" -H 'Content-Type: application/json' \
  -X POST "$GRAFANA/api/dashboards/db" --data @/tmp/pg-dashboard-import.json
```

仓库自带的大盘名为 **Pgcli PostgreSQL**，UID 固定为 `pgcli-pgexporter`，第二次
导入会原地更新。Node Exporter Full 保留其 JSON 中的 UID。重复创建数据源会返回
HTTP 409，此时复用已有数据源即可。**Instance** 变量默认选中第一个 PostgreSQL
目标；管理大量数据库时可在选择器中选 `All` 或添加更多实例。

## 安全注意事项

- Prometheus、Alertmanager 和 webhook 保持 loopback。Alertmanager 的 silence API
  与通知渠道同样敏感。
- 9630 端口上的 `pgexporter` 没有认证；用主机防火墙限制在监控网段。
- 监控角色密码、Alertmanager 路由密钥和第三方 API token 都不进仓库
  （`secrets.yaml` 已 gitignored；webhook 从 0600 文件读取 token）。
- 从 `[observable]` 移除的机器会在下次 apply 时从 Prometheus 抓取列表中消失；
  它的 `nodeexporter` 容器只有显式执行 remove/absent 状态时才会删除。
