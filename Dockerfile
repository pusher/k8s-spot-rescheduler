ARG VERSION=undefined

FROM golang:1.12 AS builder
ARG VERSION

RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh

WORKDIR /go/src/github.com/pusher/k8s-spot-rescheduler

COPY Gopkg.lock Gopkg.lock
COPY Gopkg.toml Gopkg.toml

RUN dep ensure --vendor-only

COPY *.go .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-X main.VERSION=${VERSION}" -a -o k8s-spot-rescheduler github.com/pusher/k8s-spot-rescheduler

FROM alpine:3.9
RUN apk --no-cache add ca-certificates
WORKDIR /bin
COPY --from=builder /go/src/github.com/pusher/k8s-spot-rescheduler/k8s-spot-rescheduler .

ENTRYPOINT ["/bin/k8s-spot-rescheduler"]
