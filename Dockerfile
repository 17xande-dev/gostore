# Build a static binary, then ship it alone. Templates and migrations are
# go:embed'ed, so the runtime image has no assets and needs no shell.
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gostore .

# Writable directories for the two optional disk-backed stores, created here with
# the runtime user's ownership. Distroless has no shell to mkdir at start-up, and
# a named volume mounted over an empty path is created root-owned — which the
# non-root process then cannot write to. Docker seeds a fresh volume from whatever
# the image has at that path, ownership included, so making them here is what
# makes `IMAGE_DIR` and `DOWNLOAD_DIR` work in a container at all.
RUN mkdir -p /images /downloads && chown -R 65532:65532 /images /downloads

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gostore /gostore
# 65532 is distroless's nonroot uid.
COPY --from=build --chown=65532:65532 /images /images
COPY --from=build --chown=65532:65532 /downloads /downloads
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/gostore"]
