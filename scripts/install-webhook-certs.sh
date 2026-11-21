#!/usr/bin/env bash
# Copyright (c) 2026 OpenFuyao Contributors.
# RootPV is licensed under Mulan PSL v2.
# You can use this software according to the terms and conditions of the Mulan PSL v2.
# You may obtain a copy of Mulan PSL v2 at:
# http://license.coscl.org.cn/MulanPSL2
# THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
# EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
# MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
# See the Mulan PSL v2 for more details.

set -euo pipefail

namespace="${NAMESPACE:-rootpv-system}"
service="${SERVICE:-persistent-rootfs-webhook}"
secret="${SECRET:-persistent-rootfs-webhook-tls}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/openssl.cnf" <<EOF
[req]
distinguished_name = req_distinguished_name
x509_extensions = v3_req
prompt = no
[req_distinguished_name]
CN = ${service}.${namespace}.svc
[v3_req]
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = ${service}
DNS.2 = ${service}.${namespace}
DNS.3 = ${service}.${namespace}.svc
DNS.4 = ${service}.${namespace}.svc.cluster.local
EOF

# Match the Helm chart's default certificate lifetime (10 years).
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
  -keyout "$tmpdir/tls.key" \
  -out "$tmpdir/tls.crt" \
  -config "$tmpdir/openssl.cnf"

kubectl -n "$namespace" create secret tls "$secret" \
  --cert="$tmpdir/tls.crt" \
  --key="$tmpdir/tls.key" \
  --dry-run=client -o yaml | kubectl apply -f -

ca_bundle="$(base64 < "$tmpdir/tls.crt" | tr -d '\n')"
kubectl patch mutatingwebhookconfiguration persistent-rootfs-mutating-webhook \
  --type json \
  -p "[{\"op\":\"replace\",\"path\":\"/webhooks/0/clientConfig/caBundle\",\"value\":\"${ca_bundle}\"}]"
kubectl patch validatingwebhookconfiguration persistent-rootfs-validating-webhook \
  --type json \
  -p "[{\"op\":\"replace\",\"path\":\"/webhooks/0/clientConfig/caBundle\",\"value\":\"${ca_bundle}\"}]"
