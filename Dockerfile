# Build a static binary, then ship it alone. Templates and migrations are
# go:embed'ed, so the runtime image has no assets and needs no shell.
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gostore .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gostore /gostore
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/gostore"]
