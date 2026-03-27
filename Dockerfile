# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-trixie@sha256:ce3f1c8d3718a306811d8d5e547073b466b15e85bfa7e1b4f0dc45516c95b72d AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
	go build -trimpath -ldflags="-s -w -buildid=" -o /out/relay ./cmd/relay

FROM gcr.io/distroless/base-debian13:nonroot@sha256:a696c7c8545ba9b2b2807ee60b8538d049622f0addd85aee8cec3ec1910de1f9

WORKDIR /app

COPY --from=build /out/relay /app/relay

EXPOSE 2525 2465 8080

USER nonroot:nonroot

ENTRYPOINT ["/app/relay"]
