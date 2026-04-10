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

FROM gcr.io/distroless/base-debian13:nonroot@sha256:fb282f8ed3057f71dbfe3ea0f5fa7e961415dafe4761c23948a9d4628c6166fe

WORKDIR /app

COPY --from=build /out/relay /app/relay

EXPOSE 2525 2465 8080

USER nonroot:nonroot

ENTRYPOINT ["/app/relay"]
