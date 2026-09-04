# JVM provisioner: installs the Eclipse Temurin JDKs we want to share, so they can
# be copied out to a host directory (deploy/local/provision-jvm.sh) and bind-mounted
# read-only into every workspace at /usr/lib/jvm. This keeps the workspace image
# slim while sharing one copy of the JDKs across all containers.
#
# To change the offered versions, edit the `for v in ...` list (and re-provision).
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      curl gnupg ca-certificates \
 && mkdir -p /etc/apt/keyrings \
 && curl -fsSL https://packages.adoptium.net/artifactory/api/gpg/key/public | gpg --dearmor -o /etc/apt/keyrings/adoptium.gpg \
 && echo "deb [signed-by=/etc/apt/keyrings/adoptium.gpg] https://packages.adoptium.net/artifactory/deb trixie main" > /etc/apt/sources.list.d/adoptium.list \
 && apt-get update \
 && for v in 8 21 25; do \
      apt-get install -y --no-install-recommends "temurin-$v-jdk" || echo "WARN: temurin-$v-jdk unavailable"; \
    done \
 # Temurin ships each JDK's lib/security/cacerts as a symlink to
 # /etc/ssl/certs/adoptium/cacerts, which lives OUTSIDE /usr/lib/jvm. Since only
 # /usr/lib/jvm is extracted and bind-mounted read-only into workspaces, that
 # target is absent there and the symlink dangles => empty truststore => Java/
 # gradle SSL handshakes fail. Replace each symlink with its real file so the
 # extracted JVM tree is self-contained.
 && for c in $(find /usr/lib/jvm -name cacerts -type l); do \
      real="$(readlink -f "$c")" && [ -f "$real" ] && cp --remove-destination "$real" "$c"; \
    done \
 && rm -rf /var/lib/apt/lists/*
