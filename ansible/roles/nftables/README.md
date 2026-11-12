# nftables

## Overview

Manage the host firewall with nftables. Debian-family hosts only.

## Task logic

`tasks/main.yml` validates inputs before touching the host; any failed check aborts the run:

- The target host must be Debian-family (`ansible_facts.os_family == 'Debian'`).
- `nftables_ssh_sources` must be a list of IPv4 addresses or CIDR prefixes.
- `nftables_ssh_sources` must be non-empty unless `nftables_ssh_allow_all: true`.
- `nftables_ssh_ports` must be a non-empty list.
- `nftables_sets` must be a list; each entry needs a `subnet` and a unique set name that starts with a letter or underscore and contains only letters, digits and underscores (not the built-in `ssh_sources`).

After validation:

1. Install nftables; when `nftables_remove_iptables_persistent` is true (default), remove netfilter-persistent / iptables-persistent.
2. Create `/etc/nftables.d` and make sure the nftables service is started.
3. Render the template to `/etc/nftables.conf`, validating it with `nft -c -f` before writing; reload nftables when the content changes.
4. Enable nftables at boot.

## Template logic

The rendered `/etc/nftables.conf` defines two tables, deleting and recreating each on reload:

- `inet _filter`
  - INPUT: `before_input` (accepts lo, established/related, DHCP replies, ICMP/ICMPv6; drops non-local destinations) -> `filter_input` (handles SSH from `nftables_ssh_sources` / `nftables_ssh_ports`; with `nftables_ssh_allow_all=false`, SSH from any other source is dropped; then jumps to `filter_default`) -> `after_input` (drops multicast/broadcast destinations) -> `logging_and_drop` (rate-limited `nft-drop:` log, then drop).
  - FORWARD: default policy from `filter_forward_default_policy`, jumps to `filter_forward`.
  - OUTPUT: policy accept, jumps to `filter_output` (empty chain).
- `inet _nat`
  - PREROUTING (priority -100) jumps to `nat_prerouting`; POSTROUTING (priority 100) jumps to `nat_postrouting` with the default policy from `net_postrouting_default_policy`.
  - The nat INPUT/OUTPUT hooks jump to `nat_input` / `nat_output` (both empty).

## Variables

### nftables_ssh_sources

List of source IPv4 addresses/CIDRs allowed to establish SSH connections. With `nftables_ssh_allow_all` false (default), connections to `nftables_ssh_ports` from any other source are dropped, even if the source is in a `nftables_sets` set. Include the control node (only emptiness and IPv4 format are validated; reachability is not).

### nftables_ssh_allow_all

When true, accept SSH from any source; `nftables_ssh_sources` may then be empty.

### nftables_ssh_ports

Host SSH port list, default `[22]`; list every port sshd listens on.

### nftables_sets

Named address sets; default `[]`. Empty sets are skipped. Rules that reference an undefined set (including a skipped empty set) are skipped too, so placeholders cannot produce an invalid ruleset. The built-in `ssh_sources` set is generated from `nftables_ssh_sources` and must not be redefined here.

### nftables_rules

Rules are inserted verbatim into the matching chain; default `[]`. Four chains are supported: `filter_default`, `filter_forward`, `nat_prerouting`, `nat_postrouting`. SSH is handled first in `filter_input` from `nftables_ssh_sources` before traffic reaches `filter_default`. With both `nftables_sets` and `nftables_rules` empty, only loopback, established/related traffic, DHCP replies, ICMP and SSH from `nftables_ssh_sources` are accepted.

### filter_forward_default_policy / net_postrouting_default_policy

Default policies for the FORWARD and POSTROUTING chains; defaults are `drop` / `accept`.

### nftables_remove_iptables_persistent

Remove netfilter-persistent / iptables-persistent when true (default). Set false when the host still relies on iptables-persistent.

## filter built-in chains

- `deny` - drop
- `nolimit` - accept
- `switch` - kill switch, point rules at it to toggle:
    sudo nft flush chain inet _filter switch
    sudo nft add rule inet _filter switch counter jump deny

## Example

```yaml
nftables_ssh_sources:
  - 10.0.0.0/8
  - 192.168.10.0/24
nftables_ssh_ports:
  - 22

nftables_sets:
  - name: cloud_inner
    subnet:
      - "10.5.0.0/16"

nftables_rules:
  # custom firewall rules
  - chain: filter_default
    rules:
      - ip saddr $cloud_inner counter jump nolimit
      - tcp dport 50001-50999 counter jump nolimit
  # dnat port 52001 to 192.168.1.2:8080
  - chain: nat_prerouting
    rules:
      - tcp dport 52001 counter dnat ip to 192.168.1.2:8080
  - chain: nat_postrouting
    rules:
      - ip daddr 192.168.1.2 tcp dport 8080 counter masquerade
```
