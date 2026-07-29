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
RUN CGO_ENABLED=0 garble -literals -tiny build -trimpath -ldflags='-s -w' -o /out/api-mock ./cmd/api-mock

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api-mock /api-mock
USER nonroot:nonroot
EXPOSE 13100
ENTRYPOINT ["/api-mock"]
