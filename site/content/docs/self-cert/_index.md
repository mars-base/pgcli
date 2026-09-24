---
title: "Self-Cert"
description: "Mint your own self-signed TLS certificates with pg cert and serve them from the store addons"
weight: 55
icon: fa-solid fa-certificate
menus:
  main:
    identifier: docs-self-cert
    parent: docs
    weight: 55
    params:
      icon: fa-solid fa-certificate
cascade:
  type: docs
  footer_style: slim
---

## Why self-sign

Two addons that speak S3 over TLS — [`minio`](../addon/minio/),
[`silo`](../addon/silo/), [`rustfs`](../addon/rustfs/) — can serve certificates
two ways:

- **Generated** (`--tls`): pgcli mints a self-signed CA + leaf for you, owns the
  cert dir, and refreshes it on restart. Zero setup, fixed SANs.
- **Bring-your-own** (`--tls-cert` / `--tls-key`): you supply the pair. This is
  where a public CA cert, a wildcard you already hold, or a cert with custom
  SANs goes.

`pg cert` is the mint for the second path: it creates a self-signed certificate
whose SANs cover exactly the hostnames and IPs you need, so you can try BYO TLS
without a real CA — and it writes them as a **standalone pair, not registered
with pgcli**: the files go wherever you point them, `pg.yaml` is untouched, and
no container is started. Hand them to a store with `--tls-cert`/`--tls-key`
yourself.

## Quick start

```bash
# A directory to hold the pair (any writable path works; keep it out of the
# data dir). pgcli's base dir already lives under ~/.pgcli:
mkdir -p ~/.pgcli/certs

# A leaf valid for one DNS name and two IPs, ECDSA P-256, 825 days (defaults):
pg cert --host "rustfs.test,127.0.0.1,10.0.0.9" \
  --cert-file ~/.pgcli/certs/rustfs.crt \
  --key-file  ~/.pgcli/certs/rustfs.key

# Serve it from a store (any of the three), giving the full paths:
pg addon install rustfs --name store \
  --tls-cert ~/.pgcli/certs/rustfs.crt \
  --tls-key  ~/.pgcli/certs/rustfs.key
```

`pg cert` prints the SANs it wrote and nothing else — it is a pure file
generator, not an addon command.

## Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--host` | `127.0.0.1` | Comma-separated DNS names and/or IPs to encode as SANs (repeatable). Entries are auto-detected as one or the other, so `"rustfs.test,10.0.0.9"` needs no special syntax; `*.wild.test` works as a wildcard DNS entry. |
| `--cert-file` | `cert.pem` | Path to write the PEM certificate. |
| `--key-file` | `key.pem` | Path to write the PEM private key (PKCS8, mode `0600`). |
| `--valid-duration` | `825` days | How long the cert stays valid, e.g. `--valid-duration 8760h` for a year. |
| `--ecdsa` | `P-256` | Curve: `P-224`/`P-256`/`P-384`/`P-521`. Set to `""` to disable ECDSA (and pair with `--rsa`). |
| `--rsa` | *(off)* | RSA key size (e.g. `2048`, `4096`); set only when you specifically need RSA instead of the default ECDSA key. |
| `--ca` | `false` | Make the cert its own CA (`CA:TRUE`, `keyCertSign`) — for when you want a private root to sign further certs with, **not** the usual case for `--tls-cert`. |

The same generator is built as a standalone binary for use outside `pg` —
`make gencert` → `bin/gencert`, with the identical flag set in single-dash form
(`-host`, `-cert-file`, …). Both front one package, so their output is the same
kind of certificate byte-for-byte.

## What `pg cert` writes

**One self-signed leaf, not a chain.** The PEM in `--cert-file` holds exactly one
`CERTIFICATE` block — the cert signs itself (`CA:FALSE`, `serverAuth` EKU, your
SANs). There is deliberately no leaf+intermediate+root bundle: there is no
issuing CA above it, so there is nothing to chain.

This is also why the `--ca` output is **not** something to point `--tls-cert`
at: `pg addon install … --tls-cert` runs `ValidateBYOCert`, which inspects the
first certificate and **rejects one that is a CA**. The `--ca` cert is a trust
anchor you sign others with, not a server cert to serve.

## Using it as the client's trust anchor

Because the result is self-signed, hand the same `.crt` to every TLS client as
its CA file — the served leaf *is* its own trust anchor. For pgBackRest's S3
repository that means pointing `--s3-ca-file` at the `pg cert` output directly:

```bash
pg backup setup --s3-endpoint <store-host>:<api-port> \
  --s3-ca-file /path/to/rustfs.crt
```

This is not a workaround pgBackRest merely tolerates: OpenSSL treats whatever
you hand it as an anchor (`CA:TRUE` not required), and pgBackRest's S3 TLS path
is that same mechanism. Verified directly: `openssl verify -CAfile <cert> <cert>`
on the self-signed `CA:FALSE` leaf returns `OK`.

To place the cert on a host that dials the store without shipping the file, use
[`pg backup fetch-ca`](../backup/#s3-object-storage-repository) — it pulls the
issuing cert straight out of the live TLS handshake.

## Serving it from a store

The three S3 stores each mount your pair under the filenames their binary
requires, so **your filenames are free** — `pg cert --cert-file anything.crt`
works for all three:

```bash
# MinIO / silo read public.crt + private.key:
pg addon install minio --name store --tls-cert anything.crt --tls-key anything.key
pg addon install silo  --name store --tls-cert anything.crt --tls-key anything.key

# rustfs reads rustfs_cert.pem + rustfs_key.pem:
pg addon install rustfs --name store --tls-cert anything.crt --tls-key anything.key
```

pgcli never re-owns or writes to your source files; in rustfs's case the pair is
mounted read-only and copied into the container's own cert dir (see
[rustfs → Privileges and ownership](../addon/rustfs/#privileges-and-ownership)).

## Verified on rustfs

On a real install, a `pg cert` leaf served by rustfs was confirmed off the live
TLS handshake (subject == issuer on the cert actually presented, not just what
`pg.yaml` recorded), the container came up healthy over HTTPS, and `pg mc`
completed a full `alias set` → `mb` → put → `ls` → get round-trip against it
with the fetched object byte-identical to the one uploaded.

## Renewing a BYO cert

A single-file bind mount pins the source inode, so replacing the file in place
does not take effect on a running container. To renew, mint a fresh pair (or
overwrite the files), then recreate:

```bash
pg cert --host "rustfs.test,127.0.0.1" --cert-file rustfs.crt --key-file rustfs.key
pg addon install rustfs --name store \
  --tls-cert rustfs.crt --tls-key rustfs.key --force
```

**Turning BYO off** (fall back to the generated pair, or plain HTTP): delete the
`cert_file`/`key_file` lines for that addon in `pg.yaml` and `--force` a
recreate.

## See also

- [`minio`](../addon/minio/) · [`silo`](../addon/silo/) ·
  [`rustfs`](../addon/rustfs/) — each has a "Bring your own certificate" section
- [Backup → S3 object storage repository](../backup/#s3-object-storage-repository)
  — the `--s3-ca-file` / `fetch-ca` client side
