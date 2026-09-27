# syntax=docker/dockerfile:1

########## 构建阶段 ##########
FROM golang:1.26-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# sqlite 驱动（mattn/go-sqlite3）需要 CGO，故此处开启 CGO 并依赖镜像内自带的 gcc
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/reader ./cmd/reader

########## 运行阶段 ##########
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai \
    UI_TYPE=webui \
    WEBUI_PORT=56789 \
    WEBUI_TOKEN= \
    CHROME_DRIVER=none \
    CHROME_DATA_DIR=/data/chrome-user-data \
    DB_URI=sqlite:///data/reader.db

WORKDIR /app

COPY --from=builder /out/reader /app/reader

# app.log 写在可执行文件同目录（/app），因此 /app 必须可写
RUN useradd --uid 10001 --home-dir /home/reader --create-home --shell /usr/sbin/nologin reader \
    && mkdir -p /data/chrome-user-data \
    && chown -R reader:reader /app /data

USER reader

VOLUME ["/data"]
EXPOSE 56789

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD ["sh", "-c", "[ \"$UI_TYPE\" = webui ] || exit 0; curl -fsS -o /dev/null \"http://127.0.0.1:${WEBUI_PORT:-56789}/\""]

ENTRYPOINT ["/app/reader"]
