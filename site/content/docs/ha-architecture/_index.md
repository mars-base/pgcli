---
title: "HA Architecture"
description: "Reference designs for same-city PostgreSQL high availability and two-site, three-center disaster recovery"
weight: 46
icon: fa-solid fa-diagram-project
menus:
  main:
    identifier: docs-ha-architecture
    parent: docs
    weight: 46
    params:
      icon: fa-solid fa-diagram-project
cascade:
  type: docs
  footer_style: slim
---

This page presents two reference designs using PostgreSQL, Patroni, etcd DCS, and HAProxy: same-city high availability, and two-site, three-center disaster recovery focused on takeover after a same-city outage.

> **Terminology:** “Same-city dual-site” here means that database members run in two same-city failure domains and Patroni manages service across them. It does not mean multi-primary writes. A Patroni cluster has one leader accepting writes at a time; other members are replicas. Simultaneous writes on multiple nodes require a separate multi-primary database design and are outside Patroni HA.

## Architecture 1: Same-city dual-site HA (single-writer)

Deploy one PostgreSQL/Patroni member in each of two same-city centers. A third PostgreSQL member may be used as an additional replica. Use three etcd members on separate hosts and, where possible, separate failure domains; placing all three members on one host or in one failure domain defeats the quorum design.

| Location | PostgreSQL / Patroni | etcd DCS | Purpose |
|---|---:|---:|---|
| Same-city center A | 1 member | 1 member | Leader or replica |
| Same-city center B | 1 member | 1 member | Replica or failover candidate |
| Independent host / third failure domain | Optional 1 member | 1 member | Third DCS vote; add a PG member according to capacity and recovery goals |
| **Total** | **At least 2 members** | **3 members** | etcd quorum requires 2 votes |

| Failure | DCS quorum | Expected behavior |
|---|---|---|
| One etcd member fails | 2/3 votes remain | DCS continues to operate |
| One PG member fails | Depends on replica health | Patroni may elect a new leader if DCS is available and a replica is eligible for promotion |
| Two DCS members fail | 1/3 vote remains | DCS loses quorum; safe automatic election is unavailable |
| Network partition between centers | The side holding 2 votes continues | The minority cannot renew the leader lock; prevent clients from bypassing the proxy and reaching an isolated old leader |

### Client endpoint

HAProxy can provide a stable PostgreSQL TCP endpoint in the same city. It checks Patroni's REST API and routes write connections to the current leader. HAProxy is a proxy, not an L2 device, and does not move a VIP across subnets.

When database hosts are on different subnets, give each proxy a fixed address on its own network. For proxy redundancy, deploy multiple HAProxy instances and configure application/connection-pool endpoints as primary and standby, or use DNS/GSLB/load balancing for a unified endpoint. Do not treat two proxy addresses as two independently writable database endpoints.

## Architecture 2: Two-site, three-center disaster recovery

The goal is **takeover after a complete same-city outage**. Deploy two PostgreSQL/Patroni members and two etcd members across the two same-city centers. Deploy one asynchronous PostgreSQL/Patroni replica and three etcd members in the third, remote center. Run the three remote etcd members on separate hosts; if they share one rack, that rack is a common failure domain.

| Site | Center | PostgreSQL / Patroni | etcd DCS | Purpose |
|---|---|---:|---:|---|
| Same city | A | 1 member | 1 member | Production cluster member |
| Same city | B | 1 member | 1 member | Production cluster member |
| Remote site | C | 1 asynchronous replica | 3 members | Retain DCS quorum and provide a DR takeover candidate after a complete same-city outage |
| **Total** | **3 centers** | **3 members** | **5 members** | etcd quorum requires 3 votes |

| Failure | Remaining DCS votes | Expected behavior and limitations |
|---|---:|---|
| One same-city center (A or B) fails | 4/5 | DCS retains quorum; the remaining PG member continues or fails over according to Patroni state |
| Both same-city centers fail | Remote 3/5 | The remote site retains quorum; if its PG/Patroni and network are healthy and the replica is eligible, it can be promoted automatically |
| Entire remote center C fails | Same city 2/5 | Same city loses quorum; this design intentionally prioritizes remote takeover after a same-city outage and does not guarantee automatic election after a remote-site outage |
| Inter-site network partition | Remote 3/5, same city 2/5 | The remote site is the quorum side; same-city Patroni cannot renew the leader lock. Validate client-path isolation and split-brain protections |

With asynchronous replication, the remote replica may not have received the latest WAL when a failure occurs, so promotion can lose data. Actual RPO depends on replication lag at the time of failure. DCS quorum is necessary for remote takeover but does not guarantee that the replica is promotable. Monitor replication lag and choose any synchronous-replication policy according to the business RPO and its latency/availability trade-offs.

### Two-site client routing and failover

HAProxy can be deployed in both the same-city and remote sites. It routes to the current leader among database members; it does not itself move application traffic from the same-city endpoint to the remote endpoint.

| Component | Recommended responsibility |
|---|---|
| Same-city HAProxy | Normal connection endpoint; probes Patroni leader state |
| Remote HAProxy | Remote endpoint; routes to the new leader after remote promotion |
| Application / connection pool | Configure the two proxy addresses as primary and standby, with reconnect support; do not write through both concurrently |
| Optional DNS/GSLB/global load balancer | Provides site-level traffic switching if the application lacks multi-endpoint support; account for health checks, DNS caches, and reconnecting existing sessions |

With different site subnets, a VIP usually does not need to move between database hosts. If a single IP must be preserved, the network team must provide cross-subnet route advertisement (for example, BGP) or SDN/load-balancer control-plane switching. Ordinary Keepalived/VRRP ARP movement is limited to the same Layer-2 broadcast domain.

## Deployment and failover checklist

- Place etcd members according to the intended failure domains; verify client/peer connectivity and that odd-member quorum matches the required failure scenarios.
- Configure consistent DCS endpoints, ports, credentials, and reachable advertise addresses for all Patroni members; allow PostgreSQL and REST API traffic between members.
- Define replication policy, acceptable RPO/RTO, remote promotion policy, and the rejoin/failback procedure after the same-city site recovers.
- Exercise single-PG failure, single-DCS failure, complete same-city outage, inter-site partition, and remote recovery; verify database roles, endpoint health checks, application reconnects, and data consistency.
- HAProxy is not a backup. Retain independent backups and WAL archives for operator error, logical corruption, and failures beyond replication protection.

See [Ansible HA clusters](../ansible/ha/) for Patroni, etcd, and HAProxy configuration examples, and [Patroni HA](../ha-cluster/ha/) and [HAProxy](../addon/haproxy/) for component behavior and commands.
