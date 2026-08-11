# Junto API — multi-stage build producing a static binary on scratch.
#
# Two Stage-1 decisions pay off here and are worth naming, because the usual Dockerfile
# carries workarounds for both:
#
#   * D17 embeds time/tzdata in the binary, so this image needs no tzdata package. That was
#     not done for Docker's benefit — it was done because timezone validation would otherwise
#     depend on host tz files and pass locally while failing in a scratch container. This is
#     the container it was talking about.
#   * migrations/embed.go embeds the SQL, so the runtime image ships no migration files and
#     `migrate` cannot drift from the binary it runs beside.
#
# The result needs exactly one thing from a base image: CA certificates, for TLS to Postgres,
# Redis, SMTP and object storage.

# ---------------------------------------------------------------------------------------
FROM golang:1.26-alpine AS build

# git is needed at BUILD time only, and not for fetching: the VCS stamp Go embeds is what
# /healthz reports as its version (cmd/api/health.go). Without git in the builder, Go omits
# the stamp silently and every deploy reports itself as "dev" — a health endpoint that cannot
# tell you which build is running is most of the way to useless.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Dependencies first, as their own layer: they change far less often than source, so an
# ordinary code change reuses the module cache instead of re-downloading the graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 for a genuinely static binary — scratch has no dynamic loader, so a build
# that links libc produces an image that fails at exec with a message ("no such file or
# directory") that names the binary rather than the missing loader, and reads as a bad COPY.
#
# -trimpath keeps build-host paths out of the binary; -s -w drop the symbol table and DWARF.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# ---------------------------------------------------------------------------------------
FROM scratch

# The only thing scratch is missing that this binary needs.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=build /out/api /api
COPY --from=build /out/migrate /migrate

# Non-root by uid, since scratch has no /etc/passwd to name a user in. 65532 is the
# conventional "nonroot" id (distroless uses it), so a volume mounted from a distroless-aware
# setup has the ownership it expects.
USER 65532:65532

EXPOSE 8080

# No HEALTHCHECK instruction: Fly runs the checks declared in fly.toml against /livez and
# /readyz, and a second, differently-configured probe inside the image would be one more
# thing to keep in step for no gain. A compose-based deployment should add one — see
# docs/deploy.md.

ENTRYPOINT ["/api"]
