# Rate Limiter em Go

Implementação didática de um **Token Bucket por cliente** em Go, com middleware HTTP, controle de concorrência e remoção de clientes inativos. O algoritmo usa apenas a biblioteca padrão; não depende de um pacote de rate limiting.

> **Estado do projeto:** este repositório oferece os pacotes `internal/ratelimit` e `internal/httpapi` e seus testes. Ainda não há um servidor executável em `cmd/server`, Dockerfile, CI ou benchmarks. O exemplo abaixo mostra como integrar o middleware em um servidor do mesmo módulo. A implementação é local a um processo.

## O problema

Sem limite, um cliente pode enviar mais requisições do que uma API consegue atender. O rate limiter decide se uma requisição pode seguir para o handler e devolve `429 Too Many Requests` quando o cliente esgota sua cota. Cada cliente tem um bucket independente: o tráfego de um IP não consome os tokens de outro.

Token Bucket permite rajadas curtas até a capacidade configurada e repõe tokens com o passar do tempo. Isso torna explícitas duas escolhas: **capacidade** (tamanho máximo da rajada) e **taxa de reposição** (tokens por segundo).

## Como funciona

Cada bucket começa cheio. Uma requisição aceita consome um token. Antes de cada decisão, o bucket calcula o tempo transcorrido desde a última reposição e acrescenta `tempo em segundos × taxa`, sem ultrapassar a capacidade. Frações de token são preservadas; uma requisição só passa quando há pelo menos um token inteiro.

Por exemplo, com capacidade `3` e taxa `1` token/segundo, três requisições imediatas passam e a quarta recebe `429`. Após um segundo, há um token novo. A reposição é **preguiçosa**: ocorre quando o cliente faz uma requisição, sem ticker ou goroutine por bucket.

```text
Requisição
   |
   v
Identificar IP em RemoteAddr
   |
   v
Consultar bucket do cliente sob mutex
   +-- token disponível --> consumir --> próximo handler
   +-- bucket vazio ------> 429 + Retry-After
```

O algoritmo está em [`internal/ratelimit/bucket.go`](internal/ratelimit/bucket.go). [`internal/ratelimit/limiter.go`](internal/ratelimit/limiter.go) mantém os buckets por identificador e devolve uma `Decision` com `Allowed`, `Remaining`, `RetryAfter` e `ResetAfter`. [`internal/httpapi/middleware.go`](internal/httpapi/middleware.go) converte a decisão em comportamento HTTP.

## Concorrência

Um único `sync.Mutex` em `Limiter` protege o mapa, os buckets armazenados nele e o estado da limpeza. Assim, duas requisições simultâneas do mesmo cliente não podem consumir o mesmo token. A escolha favorece uma regra de sincronização simples e verificável; também serializa as decisões de clientes diferentes. `Bucket` isolado não oferece sincronização própria.

Os testes cobrem acesso simultâneo a um bucket e limpeza durante acesso concorrente. Os testes do algoritmo e da limpeza controlam o relógio sem pausas artificiais para verificar reposição e expiração; os testes HTTP verificam status e cabeçalhos. Execute o detector de corridas no seu ambiente com `go test -race ./...`; ele exige suporte a CGO e um compilador C adequado.

## Clientes inativos e memória

`Limiter` registra a última atividade de cada cliente, inclusive quando a requisição é rejeitada. Um bucket é considerado inativo após o **TTL de inatividade**. Quando uma requisição encontra o próprio bucket expirado, ela recebe um bucket novo e cheio, mesmo antes da próxima varredura geral.

Uma requisição que alcança o **intervalo de limpeza** varre o mapa e remove os demais buckets expirados. Tudo acontece sob o mesmo mutex. Não há goroutine de limpeza nem rotina de encerramento adicional. `NewLimiter` usa TTL de **15 minutos** e intervalo de **1 minuto**; `NewLimiterWithCleanup` permite configurar ambos. Capacidade, taxa, TTL e intervalo devem ser positivos; a taxa também deve ser finita.

Essa solução reduz o acúmulo de clientes antigos, mas não impõe um limite rígido de memória. Muitos identificadores novos em pouco tempo ainda podem criar muitos buckets, e sem novas requisições não ocorre varredura. A varredura custa `O(n)` em relação ao número de clientes armazenados e pode aumentar a latência da requisição que a dispara.

## Identificação e respostas HTTP

`RemoteAddrClientID` extrai e normaliza o IP de `RemoteAddr`, removendo a porta e aceitando IPv4 e IPv6. Um endereço inválido gera `400 Bad Request`. Cabeçalhos como `X-Forwarded-For` são ignorados porque podem ser forjados. Uma implantação atrás de proxy precisa implementar e documentar uma estratégia de **proxies confiáveis** antes de usar o IP encaminhado. O middleware também aceita uma função `ClientIdentifier` própria.

| Campo | Significado neste projeto |
| --- | --- |
| `RateLimit-Limit` | Capacidade configurada do bucket. |
| `RateLimit-Remaining` | Tokens inteiros restantes após a decisão. |
| `RateLimit-Reset` | Segundos, arredondados para cima, até o bucket ficar cheio. |
| `Retry-After` | Em `429`, segundos, arredondados para cima, até haver um token. |

Requisições aceitas seguem para o próximo handler, que escolhe o status de sucesso. Requisições negadas recebem `429 Too Many Requests` e não chegam ao handler. Os cabeçalhos `RateLimit-*` são informativos segundo as definições acima; esta implementação **não reivindica conformidade integral com uma especificação HTTP de rate limit**. Como os tempos são arredondados para segundos inteiros, `Retry-After` pode ser conservador.

## Uso em um servidor

O repositório ainda não inclui um binário de demonstração. Dentro deste módulo, um `main.go` pode usar o middleware assim:

```go
package main

import (
    "log"
    "net/http"
    "time"

    "github.com/k11ngp1ng/rate-limiter/internal/httpapi"
    "github.com/k11ngp1ng/rate-limiter/internal/ratelimit"
)

func main() {
    limiter, err := ratelimit.NewLimiterWithCleanup(3, 1, 15*time.Minute, time.Minute)
    if err != nil {
        log.Fatal(err)
    }

    mux := http.NewServeMux()
    mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = w.Write([]byte("resource\n"))
    })
    mux.Handle("GET /api/v1/resource", httpapi.RateLimit(limiter, httpapi.RemoteAddrClientID)(resource))

    log.Fatal(http.ListenAndServe(":8080", mux))
}
```

Salve o exemplo como `cmd/server/main.go` dentro do módulo e execute `go run ./cmd/server`. A rota `/health` fica fora do limite. Para observar a cota, faça quatro chamadas rápidas do mesmo cliente:

```sh
curl -i http://localhost:8080/health
curl -i http://localhost:8080/api/v1/resource
curl -i http://localhost:8080/api/v1/resource
curl -i http://localhost:8080/api/v1/resource
curl -i http://localhost:8080/api/v1/resource
```

As três primeiras chamadas ao recurso devem passar se ocorrerem antes de uma reposição; a quarta pode receber `429` com `Retry-After`. O tempo entre chamadas influencia o resultado. O exemplo é apenas uma integração mínima; um servidor de produção também precisa de configuração, timeouts, logging e encerramento gracioso.

## Testes e medição

Requer Go **1.22 ou superior**. Na raiz do repositório:

```sh
gofmt -w internal
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

Os testes em `bucket_test.go`, `limiter_test.go` e `middleware_test.go` verificam consumo, reposição fracionária, teto de capacidade, isolamento de clientes, concorrência, limpeza, status e cabeçalhos HTTP. `go test` verifica comportamento; `go test -race` detecta acessos concorrentes inseguros durante os testes. Um teste de carga ou benchmark mede desempenho, não substitui essas verificações.

Ainda não há benchmarks no repositório nem resultados publicados. Quando forem adicionados, execute-os separadamente com `go test -run '^$' -bench=. -benchmem ./internal/ratelimit` e registre comando, configuração, versão do Go e máquina antes de interpretar os números. Para gerar tráfego concorrente contra um servidor integrado, uma ferramenta externa como `hey` ou `vegeta` pode ser usada opcionalmente; compare respostas `2xx` e `429` e repita com mais de um cliente. Este README não apresenta números não medidos.

## Limitações e próximos passos

- O estado é **em memória e por processo**. Instâncias diferentes mantêm cotas independentes. Limites globais exigiriam estado compartilhado, por exemplo em Redis, fora do escopo atual.
- A identidade por IP pode agrupar usuários atrás de NAT. Cabeçalhos de proxy requerem uma política de confiança explícita.
- A limpeza por TTL controla clientes antigos, mas não limita o número de clientes ativos ou novos durante uma rajada. Um atacante pode explorar identificadores de alta cardinalidade.
- Uma varredura do mapa ocorre dentro de uma requisição. Medir esse custo antes de trocar o mecanismo de sincronização ou limpeza.
- Ainda faltam um servidor de demonstração versionado com encerramento gracioso, benchmarks, CI e empacotamento. Esses são marcos futuros, não capacidades já entregues.

O desenho privilegia código curto e comportamento observável: algoritmo próprio, dependência zero, um mutex e limpeza oportunista. Antes de otimizar, valide correção, execute o detector de corridas e meça uma carga representativa.
