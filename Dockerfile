FROM gliderlabs/alpine:3.6
#FROM golang:1.9.1-alpine3.6
#FROM ubuntu:16.04
ENTRYPOINT ["/src/entrypoint.sh"]
VOLUME /mnt/routes
EXPOSE 80
#EXPOSE 80 40000

COPY . /src/

RUN cd /src && ./build-env.sh \
       && ./build-glide.sh \
       && chmod +x /src/entrypoint.sh \
       && apk --no-cache add curl \
       &&  ./build.sh "$(cat VERSION)"

#RUN apk update \
#        && apk add git \
#        && go get github.com/derekparker/delve/cmd/dlv
#
#CMD ["/src/entrypoint_debug.sh", "&&", "dlv", "--listen=:40000", "--headless=true", "--api-version=2", "exec", "/bin/logspout"]