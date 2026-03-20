# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-trixie@sha256:96b28783b99bcd265fbfe0b36a3ac6462416ce6bf1feac85d4c4ff533cbaa473 AS build

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
