# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-trixie AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
	go build -trimpath -ldflags="-s -w -buildid=" -o /out/relay ./cmd/relay

FROM gcr.io/distroless/base-debian13:nonroot

WORKDIR /app

COPY --from=build /out/relay /app/relay

EXPOSE 2525 2465 8080

USER nonroot:nonroot

ENTRYPOINT ["/app/relay"]
