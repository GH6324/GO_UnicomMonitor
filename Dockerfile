# 使用官方 Go 镜像作为构建阶段
FROM golang:1.25-alpine AS builder

# 设置工作目录
WORKDIR /app

# 复制 go.mod 和 go.sum 文件
COPY src/go.mod src/go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY src/ .

# 构建应用
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# 使用轻量级的 alpine 作为运行阶段
FROM alpine:latest

# 安装时区数据并设置上海时区
RUN apk add --no-cache tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone
ENV TZ=Asia/Shanghai
LABEL name=unicomMonitor
LABEL url=https://github.com/zgcwkjOpenProject/GO_UnicomMonitor

# 设置工作目录
WORKDIR /root/

# 从构建阶段复制编译好的二进制文件和必要的资源
COPY --from=builder /app/main ./
COPY --from=builder /app/config.json ./
COPY --from=builder /app/static ./static/

# 暴露端口
EXPOSE 25678

# 运行应用
CMD ["./main"]
