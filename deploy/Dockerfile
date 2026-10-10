FROM node:22-bookworm-slim AS frontend
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/internal/site/dist/ internal/site/dist/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /ftthlab ./cmd/ftthlab

FROM ubuntu:26.04
ARG KVM_GID=991
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl qemu-system-x86 qemu-utils iproute2 ppp iputils-ping socat util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && if id ubuntu >/dev/null 2>&1; then userdel -r ubuntu; fi \
    && groupadd --gid 1000 fiberlab \
    && useradd --uid 1000 --gid fiberlab --create-home fiberlab \
    && (getent group "$KVM_GID" || groupadd --gid "$KVM_GID" kvm) \
    && usermod -aG "$(getent group "$KVM_GID" | cut -d: -f1)" fiberlab
COPY --from=build /ftthlab /usr/local/bin/ftthlab
COPY --chmod=755 deploy/runtime.sh /usr/local/bin/fiberlab-runtime
WORKDIR /home/fiberlab
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/ftthlab"]
CMD ["serve", "--data-dir", "/data", "--listen", "127.0.0.1:18787"]
