FROM ghcr.io/byggflow/sandbox-base:latest AS base

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git bash coreutils \
    nodejs npm \
    python3 python3-pip python3-venv \
    golang-go \
    && rm -rf /var/lib/apt/lists/*
COPY --from=base /usr/local/bin/sandbox-agent /usr/local/bin/sandbox-agent
WORKDIR /root
ENTRYPOINT ["/usr/local/bin/sandbox-agent"]
