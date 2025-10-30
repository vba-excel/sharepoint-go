# sharepoint-client – v1.0.0

## Destaques
- **Leitura robusta** de listas SPO com paginação `$skiptoken` (`--all`) e tolerância a throttling.
- **Fallback inteligente** quando há `SPQueryThrottledException` (varre sem filtro no servidor e filtra em memória se possível).
- **Controlos de timeout**
  - `--http-timeout` por pedido.
  - `--global-timeout` para a operação inteira (usar `0` para dumps completos sem limite).
- **Saídas para pipelines**: `json`, `jsonl`, `csv`, e `--clean-output` para remover `__metadata`.
- **Pós-leitura em escrita**: `add-item` e `update-item` devolvem o estado real atualizado (honrando `--select`).
- **“Últimos N registos” pronto a usar**: se usares `--top N` sem `--orderby`, assume `ID desc`.
- **Summary claro**: `items, pages, throttled, partial, fallback, stoppedEarly, topSatisfied`.

## Melhorias recentes (v1.0.0)
- `stoppedEarly` no resumo (distingue *timeout/throttle* vs “parei porque já tinha `top`”).
- `topSatisfied` mais fiel em `--all`.
- Normalização `ID` vs `Id` documentada.
- Refactor de `add/update` (pós-leitura partilhada).
- Debug do modo Edge resolvido: `useTempProfile`, `headlessFirst`, `interactiveFallback`, etc.
- Cache de cookies endurecida (ficheiro 0600) + metadados de `site`/`cachedAt` em debug.

## CLI (resumo)
```text
Modes: list-items | latest-item | get-item | add-item | update-item | delete-item
Saída: --output json|jsonl|csv   (usar --clean-output para remover "__...")

Leitura típica:
  --list <nome> --select "Id,Matricula,..." [--where/--filter ...] [--orderby ...]
  --top N      (sem --orderby → assume "ID desc")
  --all        (varre a lista toda com paginação)
  --summary    (imprime resumo técnico em stderr)

Timeouts:
  --http-timeout <s> (default 30)
  --global-timeout <s> (0 = sem limite)
```

## Autenticação
- Edge on-demand (chromedp) com cache em memória + disco (0600).
- Auto-perfil, fallback para perfil temporário se perfil real estiver locked (opcional).
- `private.json` controla as opções (ex.: `autoProfile`, `interactiveFallback`, `allowTempProfileWhenLocked`).

## Instalação binários
1. Vai à página da Release `v1.0.0`.
2. Descarrega o arquivo para o teu SO:
   - `sharepoint-client_v1.0.0_windows_amd64.zip`
   - `sharepoint-client_v1.0.0_linux_amd64.tar.gz`
   - (podem existir variantes adicionais)
3. Verifica checksum (`.sha256`) se desejares.
4. Extrai e executa o binário `sharepoint-client`/`sharepoint-client.exe`.

## Verificação de integridade
- Cada artefacto acompanha um ficheiro `<artefacto>.sha256` com o hash SHA-256.
- No Linux/macOS:
  ```bash
  shasum -a 256 sharepoint-client_v1.0.0_linux_amd64.tar.gz
  ```
- No Windows (PowerShell):
  ```powershell
  Get-FileHash .\sharepoint-client_v1.0.0_windows_amd64.zip -Algorithm SHA256
  ```

## Notas de compatibilidade
- Requer Go 1.25+ para build local.
- Edge/Chromium disponível no sistema (para o fluxo de autenticação).
- O comportamento de throttling do SPO pode variar; ver `--summary`.
