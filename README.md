# FlowGate

L4 TCP/UDP-прокси и load balancer на Go. Минимальные зависимости, статический бинарь, нацелен на production-нагрузки с низкой латентностью.

## Возможности (Phase 1)

- **TCP и UDP** проксирование с per-route таймаутами и буферами из пула
- **Балансировщики:** Smooth Weighted Round-Robin, Least Connections
- **PROXY Protocol v1 и v2** (auto-detect, режимы `off | v1 | v2 | auto`)
- **Graceful shutdown** на SIGINT/SIGTERM с настраиваемым таймаутом
- **Несколько routes** в одном инстансе (TCP + UDP параллельно)
- **Structured logging** через `log/slog` (text/json), `instance_id` + `version` в каждой записи

## Быстрый старт

```bash
# build
make build

# run with the sample dev config
CONFIG_PATH=$(pwd)/config/config.dev.yaml ./bin/flowgate
```

Дефолтный `config/config.dev.yaml` поднимает:
- TCP route `tcp-edge` на `:18080`, RR, 2 бэкенда (веса 5/3)
- UDP route `udp-edge` на `:18081`, LeastConn, 2 бэкенда

## Конфигурация

YAML, путь задаётся через `CONFIG_PATH` (можно через `.env` в рабочей директории). Минимальный пример:

```yaml
env: dev
log_level: info
server:
  shutdown_timeout: 10s
defaults:
  connect_timeout: 5s
  idle_timeout: 60s
  max_conns: 1024
  buf_size: 32768
routes:
  - name: tcp-edge
    protocol: tcp
    listen: ":8080"
    balancer: round_robin       # round_robin | least_conn
    proxy_protocol: off         # off | v1 | v2 | auto
    backends:
      - { addr: "10.0.0.1:80", weight: 1 }
      - { addr: "10.0.0.2:80", weight: 1 }
```

Полный набор полей и UDP-секция — в `config/config.dev.yaml`.

## Разработка

```bash
make test              # unit-тесты с -race + coverage
make test-integration  # E2E (build tag integration), нужен ~5s
make lint              # golangci-lint
make ci                # полный pipeline: tidy → fmt → vet → lint → tests → build
```

CI (`.github/workflows/ci.yml`) гоняет `lint`, `Test (unit)`, `Test (integration)` параллельно и `build` следом.

## Архитектура

```
cmd/main.go                  → entrypoint (config + logger + app.Run)
internal/app/                → wiring routes, lifecycle через pkg/closer
internal/config/             → cleanenv + godotenv, Route.Effective(Defaults)
internal/balancer/           → RR (smooth weighted) + LeastConn
internal/registry/           → in-memory backend pool с atomic snapshot
internal/proxy/tcp/          → listener + accept loop + handler + pipe pair
internal/proxy/udp/          → listener + session table с TTL GC
internal/proxy/proxyproto/   → парсеры v1/v2 + conn wrapper
internal/pool/               → sync.Pool buffer pool
pkg/closer/                  → graceful shutdown (signals, ordered close, errors.Join)
pkg/logger/                  → slog setup, version injection через -ldflags
```

## Roadmap

| Фаза | Цель | Статус |
|---|---|---|
| **1. L4 Core** | TCP/UDP + RR/LC + PROXY Protocol + E2E | ✅ done |
| **2. L7 HTTP Proxy** | HTTP routing (host/path/headers), backend groups, header manipulation, WebSocket | next |
| **3. Health checks** | Active/passive checks, circuit breaker, retry budget, бенчмарки | planned |
| **4. Admin API** | Runtime-управление, hot reload, rate limit, connection limits per-IP | planned |
| **5. TLS + Observability** | TLS termination/passthrough, SNI, OpenTelemetry, access logs, **v0.1.0 release** | planned |
| **6. Clustering + HTTP/2** | N-инстансов через Redis, HTTP/2 up/downstream | planned |
| **7. QUIC + Extensions** | HTTP/3, plugin architecture, Admin API auth, ACME, **v1.1** | planned |

