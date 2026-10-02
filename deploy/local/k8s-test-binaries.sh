#!/usr/bin/env bash
# k8s-test-binaries.sh — install the three binaries the kubernetes runtime adapter's
# envtest-style Go tests start: kube-apiserver, kube-controller-manager and etcd
# (control-plane/internal/runtime/runtime_kubernetes_envtest_test.go).
#
# There is no kubelet and no scheduler: the real StatefulSet controller creates and
# deletes pods, and the tests write the pod status a node would. That is enough to run
# the adapter's state machine against the real API server, its admission (Pod Security
# `restricted` included) and the real controller's timing.
#
# The tests skip when the binaries are absent, so CI stays green without them. Run this
# once per machine; it is idempotent and installs into ~/.local only:
#
#   deploy/local/k8s-test-binaries.sh            # installs, prints the directory
#   AF_K8S_TEST_BIN_DIR=<dir> go test ./internal/runtime/ -run Kubernetes
#
# The tests look in the default directory below when AF_K8S_TEST_BIN_DIR is unset.
# Every download is checked against a sha256 pinned here, not one fetched next to it.
set -euo pipefail

K8S_VERSION=v1.34.12
ETCD_VERSION=v3.6.5
DEST="${AF_K8S_TEST_BIN_DIR:-$HOME/.local/share/af-k8s-test/$K8S_VERSION}"

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "k8s-test-binaries: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

sha_of() {
  case "$ARCH:$1" in
    amd64:kube-apiserver) echo 89eea0ba75280c562f9dcb5562829d5e123fe31b4900f15c2c7d01601a976a75 ;;
    amd64:kube-controller-manager) echo f4ba7f288f35948da1bf837e5e43fc5146aa52094bd1236fa8db58409b5cb6a1 ;;
    amd64:etcd) echo 66bad39ed920f6fc15fd74adcb8bfd38ba9a6412f8c7852d09eb11670e88cac3 ;;
    arm64:kube-apiserver) echo 37ced859a8c4653a76db2780a501c8f64e71f50dc5a7ff676f2db4bba5278419 ;;
    arm64:kube-controller-manager) echo 3c22c2de8d0c8e95acff9c8a6a1e2013e8316a19fdcdc3abb69eb708f824c81f ;;
    arm64:etcd) echo 7010161787077b07de29b15b76825ceacbbcedcb77fe2e6832f509be102cab6b ;;
  esac
}

# check <file> <name>: a mismatch removes the file, so a later run downloads it again
# instead of trusting a truncated or substituted binary.
check() {
  local want got
  want="$(sha_of "$2")"
  got="$(sha256sum "$1" | cut -d' ' -f1)"
  if [ "$got" != "$want" ]; then
    rm -f "$1"
    echo "k8s-test-binaries: sha256 mismatch for $2 ($got, want $want)" >&2
    exit 1
  fi
}

mkdir -p "$DEST"
TMP="$(mktemp -d "$DEST/.dl.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

for b in kube-apiserver kube-controller-manager; do
  if [ -x "$DEST/$b" ]; then continue; fi
  echo "==> $b $K8S_VERSION ($ARCH)"
  curl -fsSL -o "$TMP/$b" "https://dl.k8s.io/release/$K8S_VERSION/bin/linux/$ARCH/$b"
  check "$TMP/$b" "$b"
  chmod 0755 "$TMP/$b"
  mv "$TMP/$b" "$DEST/$b"
done

if [ ! -x "$DEST/etcd" ]; then
  echo "==> etcd $ETCD_VERSION ($ARCH)"
  tgz="etcd-$ETCD_VERSION-linux-$ARCH.tar.gz"
  curl -fsSL -o "$TMP/$tgz" "https://github.com/etcd-io/etcd/releases/download/$ETCD_VERSION/$tgz"
  check "$TMP/$tgz" etcd
  tar -xzf "$TMP/$tgz" -C "$TMP" "etcd-$ETCD_VERSION-linux-$ARCH/etcd"
  mv "$TMP/etcd-$ETCD_VERSION-linux-$ARCH/etcd" "$DEST/etcd"
fi

echo "$DEST"
