#!/usr/bin/env bash
# Vendored from k3s-io/k3s contrib/util/generate-custom-ca-certs.sh.
#
# Changes from upstream: every -days value is driven by DAYS, so a longer-lived
# CA (for example DAYS=36500, 100 years) can be generated without editing the
# script. The upstream structure and file names are unchanged.
#
# This script generates the CA set K3s reads from <DATA_DIR>/server/tls before
# the first server start. Certificates MUST be present before K3s starts the
# first time and SHOULD NOT change afterwards.
set -e
umask 027

TIMESTAMP=$(date +%s)
PRODUCT="${PRODUCT:-k3s}"
DATA_DIR="${DATA_DIR:-/var/lib/rancher/${PRODUCT}}"
DAYS="${DAYS:-3700}"

if type -t openssl-3 &>/dev/null; then
	OPENSSL=openssl-3
else
	OPENSSL=openssl
fi

echo "Using $(type -p ${OPENSSL}): $(${OPENSSL} version)"

if ! ${OPENSSL} ecparam -name prime256v1 -genkey -noout -out /dev/null &>/dev/null; then
	echo "openssl not found or missing Elliptic Curve (ecparam) support."
	exit 1
fi

${OPENSSL} version | grep -qF 'OpenSSL 3' && OPENSSL_GENRSA_FLAGS=-traditional

mkdir -p "${DATA_DIR}/server/tls/etcd"
cd "${DATA_DIR}/server/tls"

# Temporary openssl configuration for the `ca` command.
mkdir -p ".ca/certs"
trap "rm -rf .ca" EXIT
touch .ca/index
openssl rand -hex 8 >.ca/serial

cat >.ca/config <<'EOF'
[ca]
default_ca = ca_default

[ca_default]
dir = ./.ca
database = $dir/index
serial = $dir/serial
new_certs_dir = $dir/certs
default_md = sha256
policy = policy_anything

[policy_anything]
commonName = supplied

[req]
distinguished_name = req_distinguished_name

[req_distinguished_name]

[v3_ca]
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always
basicConstraints = critical, CA:true
keyUsage = critical, digitalSignature, keyEncipherment, keyCertSign
EOF

# Never rotate the service account issuer key: it is passed to both the
# controller-manager and the apiserver, so invalidating it breaks existing
# service account tokens. Existing keys are kept ahead of the new one.
if [[ -e service.key ]]; then
	echo "Generating additional Kubernetes service account issuer RSA key"
	OLD_SERVICE_KEY="$(cat service.key)"
else
	echo "Generating Kubernetes service account issuer RSA key"
fi
${OPENSSL} genrsa ${OPENSSL_GENRSA_FLAGS:-} -out service.key 2048
echo "${OLD_SERVICE_KEY}" >>service.key

if [[ -e root-ca.pem ]]; then
	echo "Using existing root certificate"
else
	echo "Generating root certificate authority RSA key and certificate"
	${OPENSSL} genrsa ${OPENSSL_GENRSA_FLAGS:-} -out root-ca.key 4096
	${OPENSSL} req -x509 -new -nodes -sha256 -days "${DAYS}" \
		-subj "/CN=${PRODUCT}-root-ca@${TIMESTAMP}" \
		-key root-ca.key \
		-out root-ca.pem \
		-config .ca/config \
		-extensions v3_ca
fi
cat root-ca.pem >root-ca.crt

if [[ -e intermediate-ca.pem ]]; then
	echo "Using existing intermediate certificate"
else
	if [[ ! -e root-ca.key ]]; then
		echo "Cannot generate intermediate certificate without root certificate private key"
		exit 1
	fi
	echo "Generating intermediate certificate authority RSA key and certificate"
	${OPENSSL} genrsa ${OPENSSL_GENRSA_FLAGS:-} -out intermediate-ca.key 4096
	${OPENSSL} req -new -nodes \
		-subj "/CN=${PRODUCT}-intermediate-ca@${TIMESTAMP}" \
		-key intermediate-ca.key | ${OPENSSL} ca -batch -notext -days "${DAYS}" \
		-in /dev/stdin \
		-out intermediate-ca.pem \
		-keyfile root-ca.key \
		-cert root-ca.pem \
		-config .ca/config \
		-extensions v3_ca
fi
cat intermediate-ca.pem root-ca.pem >intermediate-ca.crt

if [[ ! -e intermediate-ca.key ]]; then
	echo "Cannot generate leaf certificates without intermediate certificate private key"
	exit 1
fi

# Leaf CAs for the control-plane and etcd components.
for TYPE in client server request-header etcd/peer etcd/server; do
	CERT_NAME="${PRODUCT}-$(echo ${TYPE} | tr / -)-ca"
	echo "Generating ${CERT_NAME} leaf certificate authority EC key and certificate"
	${OPENSSL} ecparam -name prime256v1 -genkey -noout -out ${TYPE}-ca.key
	${OPENSSL} req -new -nodes \
		-subj "/CN=${CERT_NAME}@${TIMESTAMP}" \
		-key ${TYPE}-ca.key | ${OPENSSL} ca -batch -notext -days "${DAYS}" \
		-in /dev/stdin \
		-out ${TYPE}-ca.pem \
		-keyfile intermediate-ca.key \
		-cert intermediate-ca.pem \
		-config .ca/config \
		-extensions v3_ca
	cat ${TYPE}-ca.pem \
		intermediate-ca.pem \
		root-ca.pem >${TYPE}-ca.crt
done

echo
echo "CA certificate generation complete. Required files are now present in:"
echo "${DATA_DIR}/server/tls"
echo "For security purposes, make a secure copy of root-ca.* and"
echo "intermediate-ca.* and remove them from cluster members:"
ls "${DATA_DIR}/server/tls/root-ca."* "${DATA_DIR}/server/tls/intermediate-ca."* | xargs -n1 echo -e "\t"