# Local-dev Postgres image with pg_textsearch (BM25) and pg_trgm.
# pg_textsearch is built from source against postgres:17 — no prebuilt image
# exists upstream and asset URL resolution is brittle, so source build is
# the more reliable path. ~30s slower build, identical runtime behavior.

# syntax=docker/dockerfile:1.7

ARG PG_TS_VERSION=v1.1.0

FROM postgres:17 AS builder

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      git ca-certificates build-essential \
      postgresql-server-dev-17 \
 && rm -rf /var/lib/apt/lists/*

ARG PG_TS_VERSION
RUN git clone --depth 1 --branch ${PG_TS_VERSION} \
      https://github.com/timescale/pg_textsearch.git /tmp/pg_textsearch

WORKDIR /tmp/pg_textsearch
RUN make && make install

FROM postgres:17

# Copy compiled extension artifacts into the runtime image.
COPY --from=builder /usr/lib/postgresql/17/lib/pg_textsearch*       /usr/lib/postgresql/17/lib/
COPY --from=builder /usr/share/postgresql/17/extension/pg_textsearch* /usr/share/postgresql/17/extension/

# pg_trgm ships with postgres-contrib in the base image; nothing to install.
