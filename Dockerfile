FROM alpine:latest

WORKDIR /
ADD ./bin/webhook /webhook
ENTRYPOINT ["./webhook"]