FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pilotreport ./cmd/pilotreport

# nonroot runs as uid 65532; 8080 is unprivileged and nothing writes to disk.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/api /api
# The weekly pilot summary, run with docker compose exec (deploy/README.md).
COPY --from=build /out/pilotreport /pilotreport
EXPOSE 8080
ENTRYPOINT ["/api"]
