# sharepoint-client

[![Release](https://img.shields.io/github/v/release/vba-excel/sharepoint-go?logo=github&sort=semver)](https://github.com/vba-excel/sharepoint-go/releases/latest)
[![Build](https://github.com/vba-excel/sharepoint-go/actions/workflows/release.yml/badge.svg)](https://github.com/vba-excel/sharepoint-go/actions/workflows/release.yml)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8)](https://go.dev/dl/)

> **Downloads & integridade**
>
> - Binários prontos: ver a secção **Releases** do GitHub.  
> - Cada release inclui um ficheiro `checksums_<versão>.txt` com os SHA‑256 de todos os artefactos.
>
> **Verificar no Linux/macOS**
> ```bash
> # Exemplo para a versão vX.Y.Z
> VER="vX.Y.Z"
> curl -sL "https://github.com/vba-excel/sharepoint-go/releases/download/${VER}/checksums_${VER}.txt" -o checksums.txt
> sha256sum -c checksums.txt   # deve dizer OK para o(s) ficheiro(s) que tens localmente
> ```
>
> **Verificar no Windows (PowerShell)**
> ```powershell
> $ver = 'vX.Y.Z'
> $zip = "sharepoint-client_$ver_windows_amd64.zip"
> $expected = (Invoke-WebRequest -UseBasicParsing "https://github.com/vba-excel/sharepoint-go/releases/download/$ver/checksums_$ver.txt").Content |
>   Select-String $zip | ForEach-Object { ($_ -split ' ')[0] }
> $actual = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
> if ($actual -eq $expected) { "OK: $zip" } else { "FALHOU: $zip`nexpected=$expected`nactual  =$actual"; exit 1 }
> ```

#
 
`sharepoint-client` é um utilitário de linha de comandos em Go para ler e escrever listas do SharePoint Online, com tolerância a throttling, paginação automática e formatos de saída pensados para pipelines (`json`, `jsonl`, `csv`).

Funciona bem com:
- Listas grandes (80k+ linhas)
- Consultas filtradas e ordenadas
- Casos de automação (integração com scripts)
- Criação / edição / remoção de itens

---

## Índice

1. [Visão geral](#visão-geral)  
2. [Configuração / pré-requisitos](#configuração--pré-requisitos)  
   - [Go](#go)  
   - [Autenticação (`private.json`)](#autenticação-privatejson)  
3. [Flags globais](#flags-globais)  
   - [Diferença entre `http-timeout` e `global-timeout`](#diferença-entre-http-timeout-e-global-timeout)  
4. [Modos de operação](#modos-de-operação)  
   - [list-items](#41-list-items)  
   - [latest-item](#42-latest-item)  
   - [get-item](#43-get-item)  
   - [add-item](#44-add-item)  
   - [update-item](#45-update-item)  
   - [delete-item](#46-delete-item)  
5. [Filtros, ordenação e limites](#5-filtros-ordenação-e-limites)  
6. [Saída / formatação](#6-saída--formatação)  
   - [stdout vs stderr](#6x-stdout-vs-stderr)
7. [Throttling, fallback e summary](#7-throttling-fallback-e-summary)  
8. [Erros e exit codes](#8-erros-e-exit-codes)
9. [Casos de uso comuns](#9-casos-de-uso-comuns)  
10. [Resumo final](#10-resumo-final)

---

## Visão geral

O binário expõe operações como:
- Ler registos (`list-items`, `latest-item`, `get-item`)
- Criar registos (`add-item`)
- Atualizar registos (`update-item`)
- Apagar registos (`delete-item`)

Foi desenhado para:
- Evitar erros de throttling do SharePoint (“excede o limiar da vista de lista”)
- Exportar dados facilmente para outras ferramentas
- Ser previsível quando pedes “os últimos N registos”
- Fazer dumps completos de listas muito grandes, se precisares

---

## Configuração / pré-requisitos

### Go

Para correr sem build:
```bash
go run ./cmd/sharepoint-client [flags...]
```

Para gerar binário:
```bash
go build ./cmd/sharepoint-client
./sharepoint-client [flags...]
```

### Autenticação (`private.json`)

O cliente não pede user/pass diretamente. Em vez disso:
1. Usa o Edge para obter cookies/tokens válidos do Microsoft 365.
2. Faz cache desses cookies (memória e disco, cifrado).
3. Usa esses cookies nas chamadas REST ao SharePoint.

O ficheiro de configuração (`private.json` por omissão) precisa de algo neste género:

```json
{
  "siteUrl": "https://TENANT.sharepoint.com/sites/NOME_DO_SITE",
  "edgeOptions": {
    "edgePath": "",
    "userDataDir": "",
    "profileDir": "",
    "headless": true,
    "timeoutSeconds": 180,
    "debug": true,

    "autoProfile": true,
    "interactiveFallback": true,
    "allowTempProfileWhenLocked": true,
    "forceTempProfile": false,
    "refreshSkewSeconds": 300
  }
}
```

#### Notas importantes sobre autenticação

- O cliente não pede credenciais. Abre (ou reutiliza) uma sessão do Microsoft Edge, lê cookies de sessão M365/SharePoint (por ex. `FedAuth`, `rtFa`) e guarda-as de forma segura:
  - **cache em memória** (válida durante a execução corrente),
  - **cache em disco cifrada** (ficheiro no `%TEMP%/gosip`, guardado com permissões 0600 para que só o utilizador atual consiga ler),
  - se ambas expirarem, volta a arrancar o Edge para renovar.

- A autenticação é feita **on-demand**. Só abrimos Edge se não houver cache válida.

- Se o teu perfil real do Edge estiver *locked* (por ex. o Edge já está aberto com a tua sessão), podemos cair automaticamente num **perfil temporário** em headless, para não tocar no teu perfil em uso e evitar erros tipo `"existing browser session"`.  
  Isto só acontece se `allowTempProfileWhenLocked=true`.

- Por omissão tentamos em modo **headless** (`headless=true`).  
  Se for preciso MFA / prompt de SSO e `interactiveFallback=true`, fazemos fallback para **janela visível** (headful) para poderes autenticar manualmente.

- Se `forceTempProfile=true`, nunca tocamos no teu perfil real; usamos sempre um perfil temporário limpo.

- Em modo `Debug=true`, escrevemos para `stderr` uma linha que explicita o modo de autenticação resolvido, por exemplo:

  ```text
  [edgeondemand] resolved edge mode:
    autoProfile=true
    headlessFirst=true
    interactiveFallback=true
    allowTempWhenLocked=true
    forceTemp=false
    useTempProfileInitially=true
    realUserData="C:\Users\alice\AppData\Local\Microsoft\Edge\User Data"
    realProfileDir="Default"
    timeout=3m0s
  ```

  Isto mostra:
  - Se vamos tentar headless primeiro.
  - Se podemos cair para janela visível.
  - Se vamos usar perfil real ou perfil temporário (e porquê).
  - Qual o timeout efetivo.

- `siteUrl` aponta para o site onde estão as listas (ex.: `/sites/LavagensJMR`).  
  Podes substituir o `siteUrl` do JSON em runtime com `--site`, sem editar o ficheiro.

---

## Flags globais

Estas flags estão disponíveis em todos os modos:

```text
-config           caminho do ficheiro de configuração (default "private.json")
-site             override do siteUrl definido no config (opcional)

-clean            limpa a cache de cookies antes (força novo login)
-clean-output     remove campos internos que começam por "__" do output final

-mode             operação:
                  list-items | latest-item | get-item | add-item | update-item | delete-item

-http-timeout     timeout em segundos por pedido HTTP individual (default 30)

-global-timeout   timeout global da operação (segundos) para leituras longas/paginadas.
                  0 = sem limite global (scan completo permitido)

-list             nome/título da lista SharePoint (ex.: "tblRegistos")

-id               ID numérico do item (get-item / update-item / delete-item)

-select           OData $select (ex.: "Id,Matricula,Operador,DataHora")

-filter           OData $filter (ex.: "Matricula eq '57RT01'")
-where            sinónimo amigável de --filter (mesma coisa)

-orderby          OData $orderby (ex.: "ID desc" ou "DataHora asc")
-sort             sinónimo amigável de --orderby

-top              limite máximo de itens a devolver
-all              se true, percorre TODAS as páginas ($skiptoken)

-latest-only      se true, devolve só o item mais recente (maior ID)
-latest           sinónimo de --latest-only

-fields           em add-item/update-item:
                  "Campo1=Valor1,Campo2=Valor2,..."

-output           formato de saída:
                  json | jsonl | csv

-summary          imprime resumo técnico no stderr no fim
-quiet            reduz verbosidade mesmo que o config tenha Debug=true
```

### Diferença entre `http-timeout` e `global-timeout`

- `--http-timeout`  
  Máximo que **cada pedido HTTP individual** pode demorar.

- `--global-timeout`  
  Tempo máximo **total da operação como um todo**, por exemplo:
  - um dump gigante com `--all` que percorre centenas de páginas,
  - um scan de uma lista de 80k+ registos.

  Se este tempo expirar:
  - a leitura é interrompida,
  - devolvemos tudo o que já tínhamos lido até esse momento,
  - e marcamos isso no resumo (`partial=true` e normalmente `stoppedEarly=true`).

- `--global-timeout 0`  
  Desativa esse limite global.  
  Útil para dumps completos de listas muito grandes (ex.: recolher 80k+ linhas).

---

## Modos de operação

### 4.1. list-items

Lê vários itens de uma lista.

Exemplo básico:
```bash
go run ./cmd/sharepoint-client \
  --mode list-items \
  --list tblRegistos \
  --select "Id,Matricula,Operador,DataHora" \
  --top 5 \
  --clean-output \
  --output json \
  --summary
```

Comportamentos importantes:

- Se usares `--top N` e **não** deres `--orderby`, o cliente força internamente `ID desc`.  
  → Recebes logo os N registos mais recentes (ID mais alto primeiro).  
  Exemplo: "dá-me os 5 últimos registos".

- Se deres `--orderby`, usamos exatamente o que definiste  
  (por ex. `--orderby "DataHora asc"` para mais antigos primeiro).

- `--filter` / `--where` aplicam um `$filter` OData.

- `--all` varre TODAS as páginas via `$skiptoken` até:
  - não haver mais páginas,
  - ou atingir `--top`,
  - ou bater timeout global.

Exemplos úteis:

Últimos 5 registos mais recentes (ordem automática `ID desc`):
```bash
go run ./cmd/sharepoint-client \
  --mode list-items \
  --list tblRegistos \
  --select "Id,Matricula,Operador,DataHora" \
  --top 5
```

Mais antigos primeiro (ordenação explícita):
```bash
go run ./cmd/sharepoint-client \
  --mode list-items \
  --list tblRegistos \
  --select "Id,Matricula,Operador,DataHora" \
  --orderby "DataHora asc" \
  --top 5
```

Filtrar pela matrícula:
```bash
go run ./cmd/sharepoint-client \
  --mode list-items \
  --list tblRegistos \
  --filter "Matricula eq '57RT01'" \
  --select "Id,Matricula,Operador,DataHora" \
  --top 5
```

Dump completo de uma matrícula específica (pode ler centenas de páginas):
```bash
go run ./cmd/sharepoint-client \
  --mode list-items \
  --list tblRegistos \
  --filter "Matricula eq '57RT01'" \
  --select "Id,Matricula,Operador,DataHora" \
  --all \
  --clean-output \
  --output json \
  --summary > dump.json
```


### 4.2. latest-item

Atalho para “dá-me o registo mais recente que corresponde ao filtro”.

Internamente:
- força comportamento equivalente a `--all` para garantir varrimento suficiente,
- força `LatestOnly=true`,
- se não houver `--orderby`, assume `ID desc` para encontrar rapidamente o maior ID.

Exemplo:
```bash
go run ./cmd/sharepoint-client \
  --mode latest-item \
  --list tblRegistos \
  --where "Matricula eq '57RT01'" \
  --select "Id,Matricula,Operador,DataHora" \
  --clean-output \
  --output json \
  --summary
```

Saída típica:
```json
{
  "DataHora": "2025-10-28T13:42:38Z",
  "ID": 86442,
  "Id": 86442,
  "Matricula": "57RT01",
  "Operador": "1006371"
}
```


### 4.3. get-item

Lê exatamente um item pelo ID interno da lista SharePoint.

```bash
go run ./cmd/sharepoint-client \
  --mode get-item \
  --list tblRegistos \
  --id 86442 \
  --select "Id,Matricula,Operador,DataHora" \
  --clean-output \
  --output json
```


### 4.4. add-item

Cria um novo registo. Os campos são fornecidos em `--fields` no formato `chave=valor`.

```bash
go run ./cmd/sharepoint-client \
  --mode add-item \
  --list tblRegTestes \
  --fields "Matricula=ABCD01,Operador=999999,Zona=JMR Centro,Site=Azambuja,DataHora=2025-10-28T19:08:38Z" \
  --select "Id,Matricula,Operador,DataHora,Zona,Site" \
  --clean-output \
  --output json
```

Como funciona internamente:
1. Faz o `POST` para criar o item.
2. Lê de volta esse item pelo ID atribuído.
3. Aplica o `--select` que deste.
4. Aplica `--clean-output` se pediste.
5. Mostra o estado final (real) do item, não apenas o payload enviado.

Se **não** deres `--select`, devolvemos todos os campos que o SharePoint retorna no GET, incluindo campos técnicos tipo `ContentTypeId`, `ParentList`, etc.

> Nota: A API REST pode aceitar criar itens mesmo sem certos campos que a UI do SharePoint considera “obrigatórios”. Ou seja, a validação da API pode ser menos rígida do que a do formulário web.


### 4.5. update-item

Atualiza campos num item existente (PATCH/MERGE).

```bash
go run ./cmd/sharepoint-client \
  --mode update-item \
  --list tblRegTestes \
  --id 2324 \
  --fields "Operador=123456" \
  --select "Id,Matricula,Operador,DataHora,Zona,Site" \
  --clean-output \
  --output json
```

Após atualizar:
1. Fazemos o PATCH/MERGE.
2. Lemos novamente o item atualizado com GET.
3. Aplicamos `--select`.
4. Devolvemos o estado final atual.

Sem `--select`, devolvemos todos os campos retornados pelo SharePoint no GET.


### 4.6. delete-item

Apaga um item concreto:

```bash
go run ./cmd/sharepoint-client \
  --mode delete-item \
  --list tblRegTestes \
  --id 2321
```

Saída:
```json
{
  "deleted": true,
  "id": 2321
}
```

---

## 5. Filtros, ordenação e limites

### 5.1. `--filter` / `--where`

`--filter` e `--where` são equivalentes e vão direto para OData `$filter`.

Exemplo típico:
```bash
--filter "Matricula eq '57RT01'"
```

Isto também é importante para o fallback anti-throttle:
- Se o filtro for exatamente do tipo `Campo eq 'Valor'` (uma única comparação simples),
  o cliente sabe aplicar esse filtro localmente em memória se o SharePoint recusar a query por throttling.
- Se o filtro for complexo (ex.: `Campo1 eq 'X' and Campo2 gt 123`), esse fallback local pode não ser possível.

### 5.2. `--orderby` / `--sort`

Controla o OData `$orderby`.

Exemplos:
```bash
--orderby "Id desc"
--orderby "DataHora asc"
```

Se **não** especificares `--orderby` e deres `--top N`, o cliente assume `ID desc` internamente.  
Isto significa: “devolve logo os últimos registos”, mesmo que não peças explicitamente `--orderby`.

### 5.3. `--top`

- Sem `--all`:
  - Lê até `N` resultados e pára cedo.
  - O summary vai indicar `topSatisfied=true` (parou porque já tinha dados suficientes).

- Com `--all`:
  - `--top` passa a significar “limite máximo TOTAL de itens a devolver”.
  - Útil para dizer: “varre a lista inteira mas só preciso dos primeiros 200 que correspondem ao filtro; não vale a pena continuar”.
  - Se esse limite for atingido, a recolha termina cedo e `topSatisfied=true`.

---

## 6. Saída / formatação

### 6.1. `--output json`
Formato default. Saída indentada e legível.

Se houver apenas 1 item relevante (por exemplo `get-item`, `latest-item` ou `list-items` que devolveu só um), devolvemos um único objeto `{...}` em vez de um array `[ {...} ]`.

### 6.2. `--output jsonl`
Cada registo numa linha separada, estilo log, sem indentação.

Exemplo:
```text
{"Id":86442,"Matricula":"57RT01","Operador":"1006371","DataHora":"2025-10-28T13:42:38Z"}
{"Id":86272,"Matricula":"57RT01","Operador":"1006371","DataHora":"2025-10-27T10:15:51Z"}
{"Id":86215,"Matricula":"57RT01","Operador":"1050429","DataHora":"2025-10-26T01:29:01Z"}
```

Isto é perfeito para `grep`, `jq -c`, etc, porque cada linha é JSON completo.

### 6.3. `--output csv`
Gera CSV:
- Se usares `--select`, a ordem das colunas segue exatamente o `--select`.
- Se não usares `--select`, usamos as chaves do primeiro item ordenadas alfabeticamente (para estabilidade).

### 6.4. `--clean-output`
Remove todas as chaves que começam por `"__"` (por exemplo `"__metadata"`), reduzindo ruído OData interno.  
Não remove outros campos técnicos que não começam por `"__"`.

### 6.x. stdout vs stderr

- O conteúdo "útil" (registos, JSON final, CSV, etc.) sai sempre em **stdout**.
- Logs técnicos e diagnóstico (`[edgeondemand]`, `[sp][req]`, `[summary]`, erros HTTP detalhados) vão em **stderr**.

Isto significa que podes redirecionar as duas coisas separadamente, por exemplo em PowerShell:

```powershell
go run ./cmd/sharepoint-client `
  --mode list-items `
  --list tblRegistos `
  --top 5 `
  --select "Id,Matricula,Operador,DataHora" `
  --clean-output `
  --summary `
  1> output.json 2> debug.log
```

- `output.json` fica com os dados em JSON (stdout).  
- `debug.log` fica com requests/responses, summary e info de autenticação (stderr).

---

## 7. Throttling, fallback e summary

SharePoint Online pode rejeitar queries com muitos resultados (“excede o limiar da vista de lista”), devolvendo um erro `SPQueryThrottledException`.

O cliente tenta contornar isso automaticamente.

### 7.1. Fluxo normal (sem `--all`)

1. Faz a query exatamente como pediste (`$filter`, `$orderby`, `$top`).
2. Se o SharePoint responder com throttle:
   - Se o teu filtro for simples do tipo `Campo eq 'valor'`:
     - Faz uma segunda query **sem filtro no servidor**, em blocos grandes (tipicamente 200 items).
     - Filtra localmente em memória (`Campo == valor`).
     - Aplica `--top` e/ou `LatestOnly` localmente.
     - Marca `fallback=true` no summary.
   - Se o filtro for complexo (por ex. `Campo1 eq 'X' and Campo2 gt 123`), esse fallback local pode não ser seguro → devolvemos erro.

Resultado prático:  
Mesmo listas de ~80k linhas podem devolver rapidamente só os registos daquela matrícula específica.

### 7.2. Fluxo com `--all`

`--all` faz paginação com `$skiptoken` e percorre TODAS as páginas, potencialmente centenas.

Com `--all`, temos tolerância a:
- throttling a meio,
- cancels/timeout global,
- limitação por `--top`.

Comportamento:
- Se houver throttling **a meio do percurso**, paramos, marcamos `partial=true`, devolvemos o que já tínhamos apanhado até ali (sem erro fatal).
- Se houver throttling **logo na primeira página** *e* o filtro é simples `Campo eq 'valor'`:
  - ativamos varrimento "fallback": pedimos páginas **sem filtro** ao servidor,
    filtramos localmente página a página (client-side),
    e continuamos a tentar varrer.
  - Isto marca `fallback=true`.

É possível ver `partial=true` mas, na prática, já ter capturado **todos** os registos que interessam.  
Exemplo real: havia ~140 registos com `Matricula eq '57RT01'`.  
A leitura parou “cedo” em termos de percorrer a lista globalmente, mas já tínhamos esses ~140 registos.  
Tecnicamente `partial=true` porque não percorremos a lista inteira, mas o resultado útil estava completo.

### 7.3. `--global-timeout`

Se definires `--global-timeout` com um valor >0:
- Se a operação demorar mais que esse limite (por exemplo: dump de 80k linhas com `--all`),
  interrompemos.
- Devolvemos o que já tínhamos até esse momento.
- `partial=true`.
- `stoppedEarly=true`.

Se usares `--global-timeout 0`:
- Não há limite global de tempo.
- Bom para dumps totais.
- Se terminar sem interrupções:
  - `partial=false`
  - `stoppedEarly=false`

### 7.4. O summary

Se usares `--summary`, no fim imprimimos em **stderr** algo como:

```text
[summary] items=125 pages=392 throttled=true partial=true fallback=true stoppedEarly=true topSatisfied=false
```

Significado de cada campo:

- `items`  
  Quantos itens foram devolvidos no `stdout` final.

- `pages`  
  Quantas páginas pedimos ao SharePoint (inclui chamadas paginadas via `$skiptoken`).

- `throttled`  
  Em algum momento o SharePoint respondeu com `SPQueryThrottledException` (limite de view / restrição de carga).

- `fallback`  
  true se tivemos de usar a estratégia de fallback:
  - pedir páginas sem filtro server-side,
  - filtrar nós próprios em memória (ex.: `Campo eq 'Valor'`),
  - e/ou usar pesquisa local para encontrar o “mais recente”.

- `partial`  
  true se **não percorremos a lista até ao fim natural**.

  Isto pode acontecer por vários motivos:
  - apanhámos throttling a meio e parámos em vez de falhar,
  - atingimos `--global-timeout` e interrompemos,
  - parámos cedo porque já reunimos resultados suficientes.

  Atenção: `partial=true` NÃO quer automaticamente dizer “faltam resultados do teu filtro”.  
  Pode simplesmente querer dizer “decidimos não ler o resto da lista inteira porque já chegava”.

- `stoppedEarly`  
  true se terminámos a recolha antes do fim completo por um motivo externo (timeout global, throttling forte, etc.).

  Em termos práticos:
  - `stoppedEarly=true` quase sempre significa que houve interrupção forçada.
  - `stoppedEarly=false` significa que ou percorremos tudo, ou parámos apenas porque já tinhas `--top` satisfeito.

- `topSatisfied`  
  true se (na perspetiva do cliente) o pedido foi cumprido.

  Exemplos:
  - Pediste `--top 5`: apanhámos 5 → `topSatisfied=true`.
  - Pediste todos os IDs (`--all --global-timeout 0`) e lemos a lista inteira → `topSatisfied=true`.
  - Pediste uma matrícula rara, e lemos o suficiente para obter todos os registos dessa matrícula antes de desistir → muitas vezes continua `topSatisfied=true`.

  Já `topSatisfied=false` aparece em cenários tipo:
  - Pediste `--all` filtrado e sabíamos (pelo SharePoint) que existiam mais páginas para percorrer, mas parámos cedo por throttling/timeout antes de as ler.

---

## 8. Erros e exit codes

Quando algo corre mal (por exemplo `--id` não existe, ou o SharePoint devolve 404), o comando:
- escreve um objeto JSON de erro em **stdout**, com estrutura estável,
- sai com código `1`.

Exemplo real de `get-item` para um ID inexistente:

```json
{
  "ok": false,
  "mode": "get-item",
  "error": "get-item falhou: unable to request api: 404 Not Found :: {\"error\":{\"code\":\"-2130575338, System.ArgumentException\",\"message\":{\"lang\":\"pt-PT\",\"value\":\"O item não existe. Pode ter sido eliminado por outro utilizador.\"}}}"
}
```

Campos:
- `ok`: será sempre `false`.
- `mode`: o modo que falhou (`get-item`, `update-item`, etc.).
- `error`: mensagem detalhada, normalmente inclui o status code do SharePoint.

Isto facilita automação porque consegues inspecionar `mode` e `error` direto em JSON sem teres de fazer parse do texto do `stderr`.

> Nota: o `stderr` continua a ter logs técnicos (`[sp][req]`, `[summary]`, `[edgeondemand]`...), mas o objeto JSON de erro está em `stdout`. Ou seja, para scripts basta testares o exit code.

---

## 9. Casos de uso comuns

### 9.1. Últimos 5 registos mais recentes de uma lista grande
```bash
go run ./cmd/sharepoint-client \
  --mode list-items \
  --list tblRegistos \
  --select "Id,Matricula,Operador,DataHora" \
  --top 5 \
  --clean-output \
  --output json \
  --summary
```
Nota: se não deres `--orderby`, o cliente força `ID desc` internamente.

### 9.2. Último registo de uma matrícula específica
```bash
go run ./cmd/sharepoint-client \
  --mode latest-item \
  --list tblRegistos \
  --where "Matricula eq '57RT01'" \
  --select "Id,Matricula,Operador,DataHora" \
  --clean-output \
  --output json \
  --summary
```

### 9.3. Todos os registos de uma matrícula específica
```bash
go run ./cmd/sharepoint-client \
  --mode list-items \
  --list tblRegistos \
  --filter "Matricula eq '57RT01'" \
  --select "Id,Matricula,Operador,DataHora" \
  --all \
  --clean-output \
  --output json \
  --summary > dump.json
```

### 9.4. Dump completo de IDs da lista inteira (muito grande)
```bash
go run ./cmd/sharepoint-client \
  --mode list-items \
  --list tblRegistos \
  --select "Id" \
  --all \
  --clean-output \
  --output json \
  --global-timeout 0 \
  --summary > dump.json
```

Isto tenta ler absolutamente tudo até ao fim da lista, sem limite de tempo global.

### 9.5. Criar um registo e ver logo como ficou
```bash
go run ./cmd/sharepoint-client \
  --mode add-item \
  --list tblRegTestes \
  --fields "Matricula=ABCD01,Operador=999999,Zona=JMR Centro,Site=Azambuja,DataHora=2025-10-28T19:08:38Z" \
  --select "Id,Matricula,Operador,DataHora,Zona,Site" \
  --clean-output \
  --output json
```

### 9.6. Atualizar só um campo específico
```bash
go run ./cmd/sharepoint-client \
  --mode update-item \
  --list tblRegTestes \
  --id 2324 \
  --fields "Operador=123456" \
  --select "Id,Matricula,Operador,DataHora,Zona,Site" \
  --clean-output \
  --output json
```

### 9.7. Apagar um registo
```bash
go run ./cmd/sharepoint-client \
  --mode delete-item \
  --list tblRegTestes \
  --id 2321
```

Saída:
```json
{
  "deleted": true,
  "id": 2321
}
```

---

## 10. Resumo final

Esta versão do `sharepoint-client` oferece:

- **Leitura robusta**  
  Inclui paginação `$skiptoken` com `--all` e tolerância a throttling.

- **Fallback inteligente ao throttling**  
  Se o SharePoint recusar uma query filtrada (`SPQueryThrottledException`), tentamos novamente sem filtro no servidor e filtramos nós mesmos em memória, sempre que possível (`Campo eq 'valor'`).

- **Controlo de timeouts**  
  `--http-timeout` (por pedido) e `--global-timeout` (para a operação inteira).  
  `--global-timeout 0` significa "vai até ao fim custe o que custar".

- **Saídas fáceis de consumir**  
  `--output json`, `--output jsonl` (1 linha por registo), ou `--output csv`.  
  `--clean-output` remove ruído interno OData (`__metadata`, etc.).

- **Pós-leitura automática em add/update**  
  `add-item` e `update-item` fazem GET depois da operação e devolvem o estado real atualizado do item.  
  Se passares `--select`, já recebes só os campos que te interessam.

- **Comportamento "últimos N registos" pronto a usar**  
  Se pedes `--top N` sem `--orderby`, o cliente assume `ID desc` para te devolver logo os registos mais recentes.

- **Resumo técnico (`--summary`) claro**  
  Mostra: nº de páginas pedidas, throttling, se houve fallback, se a listagem terminou cedo por `--top`, se houve timeout global, etc.  
  Campos como `partial`, `stoppedEarly` e `topSatisfied` ajudam-te a perceber se tens dados completos ou parciais, e porquê.

- **Erros consistentes em JSON**  
  Em caso de erro devolvemos `{ "ok": false, "mode": "...", "error": "..." }` em stdout e saímos com exit code 1.  
  Fácil de automatizar em scripts.

Em resumo: dá-te uma forma estável, previsível e scriptável de interagir com listas do SharePoint Online, mesmo quando essas listas são grandes e o SharePoint começa a fazer throttle.
