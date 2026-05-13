# FlowGate

L4 TCP/UDP-прокси и load balancer на Go. Минимальные зависимости, статический бинарь, нацелен на production-нагрузки с низкой латентностью. Работа над L7 HTTP-слоем уже идёт — см. [Roadmap](#roadmap).

## Возможности

### L4 (готово)

- **TCP и UDP** проксирование с per-route таймаутами и буферами из `sync.Pool`
- **Балансировщики:** Smooth Weighted Round-Robin, Least Connections (атомарные счётчики, без блокировок на выборе)
- **PROXY Protocol v1 и v2** (auto-detect, режимы `off | v1 | v2 | auto`)
- **Concurrency limiter** на route (семафор по `max_conns`)
- **Exponential backoff** на retriable accept-ошибках (`EMFILE`, `ENFILE`, `ECONNABORTED`)
- **Graceful shutdown** на SIGINT/SIGTERM с настраиваемым таймаутом и обратным порядком закрытия
- **Несколько routes** в одном инстансе (TCP + UDP параллельно)
- **Structured logging** через `log/slog` (text/json), `instance_id` + `version` в каждой записи

### L7 HTTP (в работе)

- ✅ **Конфиг и валидация** HTTP-route: `backend_groups`, `routing_rules`, `headers`, `standard_headers`, `websocket`, `timeouts`
- ✅ **Router**: матчинг по host, path (`exact | prefix | regex`), headers, query params; приоритезация (exact → regex → longest-prefix → fallback)
- ⏳ **HTTPRunner и регистрация в `proxy.New`** — TODO; пока конфиг с `protocol: http` валидируется, но фабрика proxy уронит старт. Закомментированный пример роута лежит в `config/config.dev.yaml`.

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

`ENV` и `LOG_LEVEL` можно переопределить переменными окружения; `CONFIG_PATH` — обязательный, читается из `.env` или окружения.

## Конфигурация

YAML через `cleanenv` + `godotenv`. Минимальный пример L4:

```yaml
env: dev
log_level: info

server:
  instance_id: fg-node-1
  shutdown_timeout: 10s

defaults:
  connect_timeout: 5s
  idle_timeout: 60s
  keepalive: 30s
  max_conns: 1024
  buf_size: 32768
  proxyproto_header_timeout: 3s
  backoff:
    base: 100ms
    max: 5s
  udp:
    session_idle: 30s
    backend_read: 5s
    dial: 2s
  http:
    response_header_timeout: 30s
    write_timeout: 0s
    read_header_timeout: 5s

routes:
  - name: tcp-edge
    protocol: tcp                 # tcp | udp | http
    listen: ":8080"
    balancer: round_robin         # round_robin | least_conn
    proxy_protocol: off           # off | v1 | v2 | auto
    timeouts:
      connect_timeout: 5s
      idle_timeout: 60s
      keepalive: 30s
    backends:
      - { addr: "10.0.0.1:80", weight: 1 }
      - { addr: "10.0.0.2:80", weight: 1 }
```

`Route.Effective(Defaults)` склеивает per-route таймауты с дефолтами (per-route выигрывает, если ненулевое). Полный набор полей — в `config/config.dev.yaml`; HTTP-секция (backend_groups, routing_rules, header rules) приведена там же закомментированной.

## Разработка

```bash
make test              # unit-тесты с -race + coverage
make test-integration  # E2E (build tag integration), нужен ~5s
make lint              # golangci-lint v2
make mocks             # перегенерация mockery
make ci                # tidy → fmt-check → vet → lint → unit → integration → build
```

CI (`.github/workflows/ci.yml`) гоняет `lint`, `Test (unit)`, `Test (integration)` параллельно и `build` следом.

## Архитектура

### Поток L4-запроса

```
                ┌──────────────┐
 client ───────▶│   Listener   │  net.ListenConfig, accept-loop с backoff на EMFILE/ENFILE
                └──────┬───────┘
                       │
                       ▼
                ┌──────────────┐
                │   Limiter    │  semaphore по defaults.max_conns
                └──────┬───────┘
                       │
                       ▼
                ┌──────────────┐
                │   Handler    │  PROXY Protocol parse (off|v1|v2|auto), таймауты route.Effective()
                └──────┬───────┘
                       │
                       ▼
                ┌──────────────┐      ┌──────────────────┐
                │   Balancer   │ ───▶ │  registry.Load() │  atomic snapshot, версионирование
                │ Pick/Release │      └──────────────────┘
                └──────┬───────┘
                       │
                       ▼
                ┌──────────────┐
                │ Dial backend │  pipe pair с буферами из sync.Pool (TCP), session-table + TTL GC (UDP)
                └──────────────┘
```

Lifecycle: `app.Run` собирает routes, регистрирует `proxy.Shutdown` в `pkg/closer` (LIFO + `errors.Join`); SIGINT/SIGTERM → graceful shutdown с таймаутом из `server.shutdown_timeout`, второй сигнал — форс-выход.

### Структура каталогов

```
flowgate/
├── cmd/
│   └── main.go                      entrypoint: config → logger → app.Run
├── internal/
│   ├── app/                         wiring routes, регистрация shutdown'ов в closer
│   ├── config/
│   │   ├── config.go                cleanenv + godotenv, Route.Effective(Defaults)
│   │   ├── http.go                  HTTP route schema (groups, rules, headers, ws)
│   │   └── validate.go              валидация L4 / HTTP routes
│   ├── balancer/
│   │   ├── balancer.go              Balancer interface + фабрика по Kind
│   │   ├── roundrobin.go            smooth weighted round-robin
│   │   └── leastconn.go             least connections, atomic ActiveConns
│   ├── registry/
│   │   └── memory.go                in-memory backend pool, atomic snapshot + версии
│   ├── limiter/
│   │   └── concurrency.go           channel-based semaphore (max_conns)
│   ├── backoff/
│   │   └── exponential.go           exponential backoff: Reset + ctx-aware Wait
│   ├── pool/
│   │   └── pool.go                  sync.Pool для байтовых буферов
│   ├── domain/
│   │   ├── model/backend.go         Backend (Addr, Weight, atomic ActiveConns)
│   │   └── errors/errors.go         ErrNoBackends, ErrProxyStarted, ErrInvalidProxyProtocol, ...
│   └── proxy/
│       ├── factory.go               Runner по Kind (tcp | udp | http TODO)
│       ├── tcp/                     listener + accept loop + handler + pipe pair
│       ├── udp/                     listener + session table с TTL GC
│       ├── proxyproto/              PROXY Protocol v1/v2 парсеры + conn wrapper
│       └── http/router/             matcher (host/path/headers/query) + классификация правил
├── pkg/
│   ├── closer/                      graceful shutdown: signals, ordered close, errors.Join
│   └── logger/                      slog setup, version через -ldflags
├── tests/
│   └── integration/                 E2E под build tag `integration` (TCP RR/LC, UDP, PROXY Proto, shutdown)
├── config/
│   └── config.dev.yaml              пример dev-конфига (L4 активен, HTTP закомментирован)
└── Makefile
```

## Roadmap

| Фаза | Цель | Статус |
|---|---|---|
| **1. L4 Core** | TCP/UDP + RR/LC + PROXY Protocol + E2E | ✅ done |
| **2. L7 HTTP Proxy** | Routing config + matcher + HTTPRunner + header rules + WebSocket | 🟡 in progress (config + router готовы; runner — следующий шаг) |
| **3. Health checks** | Active/passive checks, circuit breaker, retry budget, бенчмарки | planned |
| **4. Admin API** | Runtime-управление, hot reload, rate limit, connection limits per-IP | planned |
| **5. TLS + Observability** | TLS termination/passthrough, SNI, OpenTelemetry, access logs, **v0.1.0 release** | planned |
| **6. Clustering + HTTP/2** | N-инстансов через Redis, HTTP/2 up/downstream | planned |
| **7. QUIC + Extensions** | HTTP/3, plugin architecture, Admin API auth, ACME, **v1.1** | planned |
