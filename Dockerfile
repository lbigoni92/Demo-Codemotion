# Demo Codemotion · immagine multi-stage
#   docker compose up --build      (oppure: docker build -t demo-codemotion .)

# ── 1. build: binario statico, niente cgo ─────────────────────────────
ARG GO_IMAGE=golang:1.24-alpine
FROM ${GO_IMAGE} AS build
RUN go install github.com/air-verse/air@latest
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# vet e test girano durante la build: se falliscono, l'immagine non viene creata
RUN CGO_ENABLED=0 go vet ./... \
 && CGO_ENABLED=0 go test ./... \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/demo-codemotion .

# ── 2. runtime: immagine vuota (scratch), utente non root, nessuna shell ──
FROM scratch
COPY --from=build /out/demo-codemotion /demo-codemotion
ENV DEMO_HOST=0.0.0.0 \
    DEMO_PORT=8080 \
    DEMO_NO_BROWSER=1 \
    DEMO_IN_CONTAINER=1
EXPOSE 8080
USER 65532:65532
# nell'immagine non c'è curl: il binario fa da healthcheck a se stesso
HEALTHCHECK --interval=15s --timeout=3s --start-period=3s --retries=3 \
  CMD ["/demo-codemotion", "-healthcheck"]
ENTRYPOINT ["/demo-codemotion"]
