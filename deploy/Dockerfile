FROM golang:1.23-alpine AS build
ARG GARBLE_VERSION=v0.14.0
WORKDIR /src
RUN apk add --no-cache git && go install mvdan.cc/garble@${GARBLE_VERSION}
COPY go.mod ./
RUN go mod download
COPY cmd/api-mock ./cmd/api-mock
COPY internal/apimock ./internal/apimock
COPY prompts ./prompts
# Obfuscation raises the cost of static binary inspection; secrets remain runtime-only.
RUN CGO_ENABLED=0 garble -literals -tiny build -trimpath -ldflags='-s -w' -o /out/api-mock ./cmd/api-mock \
    && mkdir -p /out/data /out/outgoing-captures \
    && chown -R 65532:65532 /out/data /out/outgoing-captures \
    && chmod 700 /out/data /out/outgoing-captures

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api-mock /api-mock
# Seed writable mount points with the same numeric identity used by
# distroless's nonroot user. Empty named volumes inherit these permissions.
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/outgoing-captures /outgoing-captures
USER nonroot:nonroot
EXPOSE 13100
ENTRYPOINT ["/api-mock"]
