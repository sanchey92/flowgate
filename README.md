# FlowGate

Лёгкий L4/L7 прокси и балансировщик на Go. Статический бинарь, минимальные зависимости, ставится в `bin/` и запускается с одним YAML-конфигом.

## Возможности

- **TCP и UDP** проксирование с per-route таймаутами и буферами из `sync.Pool`
- **HTTP/1.1 reverse-proxy** на базе `httputil.ReverseProxy`: матчинг по host, path (`exact | prefix | regex`), headers и query; per-route header rules с подстановками `${client_ip}`, `${request_id}`, `${backend_addr}`
- **WebSocket** upgrade из коробки
- **Балансировка:** Smooth Weighted Round-Robin и Least Connections без блокировок на выборе бэкенда
- **PROXY Protocol v1/v2** с auto-detect (`off | v1 | v2 | auto`)
- **Concurrency limiter** на route и exponential backoff на `EMFILE`/`ENFILE`/`ECONNABORTED`
- **Graceful shutdown** в обратном порядке по SIGINT/SIGTERM
- **Structured logging** через `log/slog` (`text` или `json`)

## Быстрый старт

```bash
make build
CONFIG_PATH=$(pwd)/config/config.dev.yaml ./bin/flowgate
```

Дефолтный `config.dev.yaml` поднимает TCP RR на `:18080`, UDP LeastConn на `:18081` и HTTP route на `:18082`.

## Конфигурация

```yaml
env: dev
log_level: info

server:
  instance_id: fg-node-1
  shutdown_timeout: 10s

defaults:
  connect_timeout: 5s
  idle_timeout: 60s
  max_conns: 1024
  buf_size: 32768

routes:
  - name: tcp-edge
    protocol: tcp                 # tcp | udp | http
    listen: ":8080"
    balancer: round_robin         # round_robin | least_conn
    proxy_protocol: off           # off | v1 | v2 | auto
    backends:
      - { addr: "10.0.0.1:80", weight: 5 }
      - { addr: "10.0.0.2:80", weight: 3 }
```

HTTP route добавляет `backend_groups`, `routing_rules`, `headers` и `websocket` — полный пример лежит в [`config/config.dev.yaml`](config/config.dev.yaml). Per-route таймауты переопределяют `defaults` через `Route.Effective(Defaults)`.

`CONFIG_PATH` обязателен, читается из окружения или `.env`.

## Разработка

```bash
make test              # unit с -race
make test-integration  # E2E под build tag integration
make bench             # router + HTTP-proxy throughput
make lint              # golangci-lint v2
make ci                # полный цикл
```

CI прогоняет `lint`, unit и integration параллельно, затем `build`. Бюджет роутера на 100 правил — **<100 µs/op** — зафиксирован в `TestRouter_Route_100Rules_P99Under100us`.
