FROM scratch
# 双架构：构建时按 --platform 选对应二进制（bin/nwg-amd64 / bin/nwg-arm64）
ARG TARGETARCH
COPY bin/nwg-${TARGETARCH} /nwg
COPY www /www
ENV NWG_WWW=/www \
    NWG_DATA=/data \
    NWG_HTTP_PORT=18089 \
    NWG_AUTH=on
VOLUME ["/data"]
EXPOSE 18089
ENTRYPOINT ["/nwg"]
