# 单端口明文镜像：一个静态二进制，静态资源已 go:embed 进去，不需要基础镜像
FROM scratch
ARG ARCH=arm64
COPY bin/naswebgateway-${ARCH} /naswebgateway
EXPOSE 18080
ENTRYPOINT ["/naswebgateway"]
