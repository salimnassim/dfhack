FROM bufbuild/buf:latest AS buf

FROM alpine:3.20
RUN apk add --no-cache bash curl jq tar
COPY --from=buf /usr/local/bin/buf /usr/local/bin/buf
COPY scripts/proto-entrypoint.sh /usr/local/bin/proto-entrypoint.sh
RUN chmod +x /usr/local/bin/proto-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/proto-entrypoint.sh"]
