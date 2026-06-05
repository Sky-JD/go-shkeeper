FROM node:24-alpine AS admin-ui

WORKDIR /src/web/admin
COPY web/admin/package*.json ./
RUN npm ci
COPY web/admin ./
RUN npm run build

FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=admin-ui /src/internal/app/admin_dist ./internal/app/admin_dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/shkeeper ./cmd/shkeeper \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/chain-worker ./cmd/chain-worker \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/admin-account ./cmd/admin-account \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker-serverkey ./cmd/worker-serverkey \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deploy-check ./cmd/deploy-check \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/final-plan ./cmd/final-plan \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cutover-preflight ./cmd/cutover-preflight \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/import-legacy-accounts ./cmd/import-legacy-accounts \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/import-legacy-main-mariadb ./cmd/import-legacy-main-mariadb \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/import-legacy-json ./cmd/import-legacy-json \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cutover-audit ./cmd/cutover-audit \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/post-cutover-verify ./cmd/post-cutover-verify \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/release-audit ./cmd/release-audit \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/runtime-audit ./cmd/runtime-audit \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/goal-audit ./cmd/goal-audit

FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 shkeeper
WORKDIR /app
COPY --from=build /out/shkeeper /app/shkeeper
COPY --from=build /out/chain-worker /app/chain-worker
COPY --from=build /out/admin-account /app/admin-account
COPY --from=build /out/worker-serverkey /app/worker-serverkey
COPY --from=build /out/deploy-check /app/deploy-check
COPY --from=build /out/final-plan /app/final-plan
COPY --from=build /out/cutover-preflight /app/cutover-preflight
COPY --from=build /out/import-legacy-accounts /app/import-legacy-accounts
COPY --from=build /out/import-legacy-main-mariadb /app/import-legacy-main-mariadb
COPY --from=build /out/import-legacy-json /app/import-legacy-json
COPY --from=build /out/cutover-audit /app/cutover-audit
COPY --from=build /out/post-cutover-verify /app/post-cutover-verify
COPY --from=build /out/release-audit /app/release-audit
COPY --from=build /out/runtime-audit /app/runtime-audit
COPY --from=build /out/goal-audit /app/goal-audit
USER shkeeper

ENV SHKEEPER_LISTEN=:5000
EXPOSE 5000

CMD ["/app/shkeeper"]
