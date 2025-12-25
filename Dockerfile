# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o yamisskey-doctor .

# Runtime stage
FROM debian:trixie-slim

# Timezone
ENV TZ=Asia/Tokyo
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && \
    echo $TZ > /etc/timezone

# Install dependencies (same as yamisskey-backup)
RUN apt-get update && apt-get install -y \
    postgresql-client \
    p7zip-full \
    curl \
    bash \
    cron \
    procps \
    gettext-base \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && curl https://rclone.org/install.sh | bash

# rclone config directory
RUN mkdir -p /root/.config/rclone

# Copy binary
COPY --from=builder /app/yamisskey-doctor /usr/local/bin/

# Working directory for downloads
RUN mkdir -p /tmp/yamisskey-restore

# Entrypoint script
COPY ./docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Crontab template for scheduled verify
COPY ./docker/crontab.template /etc/cron.d/crontab.template

# Log file
RUN touch /var/log/cron.log && chmod 0644 /var/log/cron.log

# PATH
RUN echo "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" >> /etc/environment

ENTRYPOINT ["/entrypoint.sh"]
