FROM golang:1.9 AS builder
WORKDIR /go/src/github.com/pusher/k8s-spot-rescheduler
COPY . .
RUN curl https://glide.sh/get | sh \
    && glide install -v \
    && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o rescheduler

FROM scratch
COPY --from=builder /go/src/github.com/pusher/k8s-spot-rescheduler/rescheduler /bin/rescheduler

ENTRYPOINT ["/bin/rescheduler"]
