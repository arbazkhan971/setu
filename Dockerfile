# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/setu ./cmd/setu

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/setu /setu
EXPOSE 4000
USER nonroot:nonroot
ENTRYPOINT ["/setu"]
CMD ["--config", "/config.yaml"]
