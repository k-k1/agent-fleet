# The engine tools image — the shell the engine and ingest task definitions used to carry
# inline (ADR 0072; the stage-2 decision recorded in PARAMETERS-60-engines.md, "Editing this
# template").
#
# Why the scripts left the template: `60-engines.yaml` is measured against a 51,200-byte inline
# limit by `deploy/local/ecs-lifecycle-stub-test.sh` case 3b-2, and the embedded shell was its
# largest remaining movable block. A YAML comment and a `Description` cost the same as any other
# byte, so prose had already gone to `cfn/PARAMETERS-60-engines.md`; this is what was left.
#
# 🔴 What that costs, and what pays it back. The script and the template no longer ship
# together: a stack update and an image copy are two acts, and `standup.sh` copies images one
# step BEFORE it deploys the stack. An old image would otherwise run the old script while the
# service reports a steady state — the quietest failure available here. Every entry point
# therefore refuses a contract number it does not recognise (`CONTRACT`, and the gate at the top
# of each script), so the mismatch is a container that exits 78 with a sentence in its own log.
#
# Base: the same `aws-cli` image the two task definitions already pulled, so nothing about how
# the sidecar reaches S3 or SSM changes. It also replaces `curlimages/curl` on the ingest's
# fetch container — one image instead of two, both of which were pulled at `:latest`, which is
# what ADR 0071 decision 6 says this deployment does not do. The tag below is pinned for that
# reason; bump it deliberately.
#
# amd64 only: the G-family has no arm64 member, and the ingest task runs on Fargate beside it.
FROM public.ecr.aws/aws-cli/aws-cli:2.31.19

LABEL org.opencontainers.image.source="https://github.com/k-k1/agent-fleet" \
      org.opencontainers.image.description="Agent Fleet's engine fetch sidecar and ingest steps (ADR 0071, ADR 0072)" \
      org.opencontainers.image.licenses="Apache-2.0"

# jq parses the active set, and the ingest's fetch step needs curl and coreutils. The aws-cli
# image is Amazon Linux with the CLI on top; everything but jq is already in the base, and this
# is the one place to notice if that stops being true.
RUN dnf install -y jq tar gzip \
 && dnf clean all \
 && rm -rf /var/cache/dnf \
 && jq --version && curl --version | head -1 && sha256sum --version | head -1

WORKDIR /opt/af
# CONTRACT first and on its own layer: it is one line, it changes on a different schedule from
# the scripts, and each script reads it relative to its own directory (which is why the layout
# here has to match the repository's).
COPY CONTRACT /opt/af/CONTRACT
COPY fetch-models.sh ingest-fetch.sh ingest-upload.sh /opt/af/
RUN chmod +x /opt/af/*.sh

# No ENTRYPOINT and no CMD. The task definitions name the script they want, exactly as they
# already named `sh -c <inline script>`; the aws-cli base sets an ENTRYPOINT of its own that
# would otherwise turn `/opt/af/fetch-models.sh` into an argument to `aws`.
ENTRYPOINT []
CMD ["/opt/af/fetch-models.sh"]
