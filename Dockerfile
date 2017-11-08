FROM gliderlabs/alpine:3.6
ENTRYPOINT ["/src/entrypoint.sh"]
VOLUME /mnt/routes
EXPOSE 80

COPY . /src/
RUN cd /src && ./build-env.sh \
       && ./build-glide.sh \
       && chmod +x /src/entrypoint.sh \
       && apk --no-cache add curl \
       &&  ./build.sh "$(cat VERSION)"
