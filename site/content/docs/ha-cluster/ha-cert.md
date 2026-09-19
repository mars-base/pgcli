---
title: "Generating a Certificate — pg cert"
description: "pg cert mints a self-signed certificate with DNS and IP SubjectAltNames for any TLS you need without a CA — dev/test servers, internal endpoints, and a MinIO serving a bring-your-own TLS certificate. Its flags, what it writes (a single self-signed leaf, not a chain), and how it acts as its own trust anchor"
weight: 54
---

`pg cert` mints a **self-signed certificate** — its SubjectAltNames covering any
mix of DNS names and IP addresses you ask for — for any place you need TLS
without going through a CA: a dev or test server, an internal endpoint, a
service whose clients you control. The certificate it writes is a normal TLS
server leaf, usable anywhere.

The use this docs tree exercises is pgBackRest's S3 path: a Patroni cluster's
backups push to an S3 store (MinIO, typically) that must serve **TLS** —
pgBackRest refuses plaintext S3 — and pgcli's MinIO addon can serve a
**bring-your-own certificate** (`pg addon install minio --tls-cert ... --tls-key
...`). The quickest way to get a cert to try that with is `pg cert`:

```bash
pg cert --host "minio.test,127.0.0.1,10.0.0.9" \
  --cert-file minio.crt --key-file minio.key
```

Nothing is registered with pgcli — it touches no `pg.yaml` and starts no
container; it just writes the two files you name and prints the SANs. See
[MinIO → Bring your own certificate](../addon/minio/#bring-your-own-certificate---tls-cert----tls-key)
for the serving side, and the worked
[HA + self-CA MinIO example](../ha-example-minio/) for it end to end.

## What it produces

A **single self-signed leaf certificate**, not a chain:

- one `CERTIFICATE` block in `--cert-file` — the cert signs itself;
- `CA:FALSE`, extended key usage `serverAuth` — a proper TLS server leaf;
- SANs split automatically: any `--host` entry that parses as an IP lands in
  the IP SANs, the rest in the DNS SANs, so `"minio.test,10.0.0.9"` needs no
  special syntax (and `*.wild.test` works as a wildcard DNS entry).

Because it is self-signed, **the leaf is its own trust anchor** — hand the same
`.crt` to any TLS client that lets you point at a CA file or bundle (curl
`--cacert`, a browser's import, an app's `SSL_CERT_FILE` …). For pgBackRest's
S3 path specifically that means `backup.repo.s3.ca_file` /
`pg backup setup --s3-ca-file minio.crt`. This is not a workaround pgBackRest
merely tolerates: OpenSSL treats whatever you hand its trust store as an
anchor regardless of `CA:TRUE`, and pgBackRest's S3 path (curl over OpenSSL)
is that mechanism — verified directly,
`openssl verify -CAfile minio.crt minio.crt` returns `OK`.

With a real **public- or private-CA** certificate none of the self-anchor
machinery applies: a chain rooted in a trusted CA needs no `ca_file` at all,
and you already hold the issuing CA for a private one.

## Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--host` | `127.0.0.1` | Comma-separated DNS names and/or IPs for the SAN (repeatable). IPs are auto-detected; `*.wild.test` works as a wildcard. |
| `--cert-file` | `cert.pem` | Path to write the PEM certificate. |
| `--key-file` | `key.pem` | Path to write the PEM private key (PKCS8, mode `0600`). |
| `--valid-duration` | `825` days (`19800h`) | How long the cert stays valid, e.g. `--valid-duration 8760h` for a year. |
| `--ecdsa` | `P-256` | Curve: `P-224`/`P-256`/`P-384`/`P-521`. Set to `""` to disable ECDSA (then pass `--rsa`). |
| `--rsa` | *(off)* | RSA key size (e.g. `2048`, `4096`); set only when you specifically need RSA instead of the default ECDSA key. |
| `--ca` | `false` | Issue a self-signed CA (`CA:TRUE`, `keyCertSign`) instead of a server leaf — see below. |

Selecting no key type at all (`--rsa 0 --ecdsa ""`) is an error: exactly one
of them must produce the key.

## The `--ca` mode is not for `--tls-cert`

`--ca` makes the cert its own Certificate Authority — for when you want a
private root to go sign further certs with. It is **not** something to hand to
MinIO's `--tls-cert`: `ValidateBYOCert` inspects the file's first certificate
and rejects one that is a CA, because a CA's private key is a signing key, not
a server key. The default (no `--ca`) is the server leaf you do want.

## Standalone binary: `gencert`

The same generator is built as a standalone program for use outside `pg`:

```bash
make gencert          # -> bin/gencert
bin/gencert -host "minio.test,10.0.0.9" -cert-file minio.crt -key-file minio.key
```

Identical flag set, single-dash form (`-host`, `-cert-file`, …). Both front the
one `internal/certgen` package, so their output is the same kind of
certificate — `pg cert` is just the path that keeps you inside the CLI.

## Example: full BYO chain in one host

```bash
# 1. mint a cert the MinIO will serve (SANs cover how clients dial it)
mkdir -p ~/.pgcli/certs
pg cert --host "minio.test,127.0.0.1,<host-ip>" --valid-duration 8760h \
  --cert-file ~/.pgcli/certs/store.crt --key-file ~/.pgcli/certs/store.key
chmod 600 ~/.pgcli/certs/store.key

# 2. MinIO serves it over HTTPS
pg addon install minio --name store --listen 0.0.0.0 \
  --tls-cert ~/.pgcli/certs/store.crt --tls-key ~/.pgcli/certs/store.key

# 3. the cluster's backups point at it; the served leaf is its own CA file
pg backup setup --s3-endpoint <host-ip>:9000 --s3-bucket pgbackrest \
  --s3-access-key admin --s3-ca-file ~/.pgcli/certs/store.crt
```

## Related

- [MinIO](../addon/minio/) — the addon itself, and its
  [bring-your-own certificate](../addon/minio/#bring-your-own-certificate---tls-cert----tls-key)
  and [generating a test certificate](../addon/minio/#generating-a-test-certificate-pg-cert)
  sections
- [Example: HA + self-CA MinIO](../ha-example-minio/) — the whole chain, run end
  to end in an isolated environment
- [Cluster Backup](../ha-backup/) — the S3 repository and WAL archiving that
  consume this certificate
- [Patroni HA](../ha/) — the `pg ha` command set
