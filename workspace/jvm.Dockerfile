# JVM provisioner: installs the Eclipse Temurin JDKs we want to share, so they can
# be copied out to a host directory (deploy/local/provision-jvm.sh) and bind-mounted
# read-only into every workspace at /usr/lib/jvm. This keeps the workspace image
# slim while sharing one copy of the JDKs across all containers.
#
# To change the offered versions, edit the `for v in ...` list (and re-provision).
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      curl gnupg ca-certificates \
 && mkdir -p /etc/apt/keyrings \
 && curl -fsSL https://packages.adoptium.net/artifactory/api/gpg/key/public | gpg --dearmor -o /etc/apt/keyrings/adoptium.gpg \
 && echo "deb [signed-by=/etc/apt/keyrings/adoptium.gpg] https://packages.adoptium.net/artifactory/deb bookworm main" > /etc/apt/sources.list.d/adoptium.list \
 && apt-get update \
 && for v in 8 21 25; do \
      apt-get install -y --no-install-recommends "temurin-$v-jdk" || echo "WARN: temurin-$v-jdk unavailable"; \
    done \
 && rm -rf /var/lib/apt/lists/*
