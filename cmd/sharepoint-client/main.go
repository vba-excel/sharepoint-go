// sharepoint-go/cmd/sharepoint-client/main.go

package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/koltyakov/gosip"

	"sharepoint-go/internal/spapi"
	edgeondemand "sharepoint-go/strategies/edgeondemand"
)

// Nome e versão por omissão (podem ser sobrepostos por ldflags no build)
const appName = "sharepoint-client"
const defaultVersion = "v1.0.5"

// Estes três são **injetados** pelo build (ldflags -X main.buildVersion=... etc.)
// Valores de fallback para execuções locais (go run / go build sem ldflags).
var (
	buildVersion = defaultVersion
	buildCommit  = "dev"
	buildDate    = ""
)

func main() {
	// Logger "limpo": sem timestamps/flags default, e sempre em stderr.
	log.SetFlags(0)
	log.SetOutput(os.Stderr)

	// --- Flags comuns ---
	configPath := flag.String("config", "private.json", "ficheiro de configuração (contém siteUrl e edgeOptions)")
	siteOverride := flag.String("site", "", "override do siteUrl (opcional)")
	cleanCache := flag.Bool("clean", false, "limpar cache de cookies antes (força re-login)")
	cleanOutput := flag.Bool("clean-output", false, "remover campos internos (__metadata, etc.) do output final")

	mode := flag.String("mode", "list-items", "ação: list-items|latest-item|get-item|add-item|update-item|delete-item")
	versionFlag := flag.Bool("version", false, "mostra a versão e sai")

	httpTimeoutSec := flag.Int("http-timeout", 30, "timeout HTTP em segundos para chamadas SharePoint (default 30)")
	globalTimeoutSec := flag.Int("global-timeout", 60, "timeout TOTAL da operação em segundos (0 = sem limite)")

	listName := flag.String("list", "", "nome/título da lista ou server-relative URL da lista")
	idFlag := flag.Int("id", 0, "ID do item (get-item/update-item/delete-item)")

	selectFields := flag.String("select", "", "OData $select (ex.: \"Id,Matricula,Operador,DataHora\")")

	filterFlag := flag.String("filter", "", "OData $filter (ex.: \"Matricula eq '57RT01'\")")
	whereFlag := flag.String("where", "", "atalho para --filter (sinónimo mais amigável)")

	orderByFlag := flag.String("orderby", "", "OData $orderby (ex.: \"ID desc\")")
	sortFlag := flag.String("sort", "", "atalho para --orderby (sinónimo mais amigável)")

	top := flag.Int("top", 0, "$top limite TOTAL de items a devolver (quando usado com --all, é o máximo total a devolver)")
	allFlag := flag.Bool("all", false, "se true, percorre todas as páginas da lista e agrega tudo; pode ser combinado com --top")

	latestOnlyFlag := flag.Bool("latest-only", false, "devolve só o item mais recente (maior ID); compatibilidade retro")
	latestFlag := flag.Bool("latest", false, "atalho para --latest-only (mais recente)")

	summaryFlag := flag.Bool("summary", false, "mostra resumo técnico (páginas pedidas, throttling, etc.) em stderr no fim")

	fieldsFlag := flag.String("fields", "", "para add-item/update-item: campos no formato key=value,key2=value2,...")

	outputFmt := flag.String("output", "json", "formato da saída: json|jsonl|csv")
	quiet := flag.Bool("quiet", false, "modo silencioso (minimiza logs em stderr; útil para pipelines)")

	flag.Parse()

	// --version: mostra metadados de build em JSON e sai
	if *versionFlag {
		v := map[string]string{
			"name":    appName,
			"version": buildVersion,
			"commit":  buildCommit,
			"date":    buildDate,
			"go":      runtime.Version(),
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(b))
		os.Exit(0)
	}

	// Unificar aliases/atalhos
	resolvedFilter := firstNonEmpty(*filterFlag, *whereFlag)
	resolvedOrderBy := firstNonEmpty(*orderByFlag, *sortFlag)
	resolvedLatest := *latestOnlyFlag || *latestFlag

	// açúcar 1: latest → se não deste --orderby, impomos "ID desc"
	if resolvedLatest && resolvedOrderBy == "" {
		resolvedOrderBy = "ID desc"
	}
	// açúcar 2: se pediste --top N e não deste --orderby e não estás a usar --all,
	// assumimos "ID desc" para devolver por defeito os registos mais recentes.
	if !resolvedLatest && *top > 0 && resolvedOrderBy == "" && !*allFlag {
		resolvedOrderBy = "ID desc"
	}

	// --- Ler config + normalizar edge options ---
	cfg := &edgeondemand.AuthCnfg{}
	if err := cfg.ReadConfig(*configPath); err != nil {
		fatalJSON("init", err)
	}
	if cfg.EdgeOptions == nil {
		cfg.EdgeOptions = &edgeondemand.EdgeConfig{}
	}

	// override de site, se fornecido
	if *siteOverride != "" {
		cfg.SiteURL = *siteOverride
	}

	// Pequenos defaults locais (mantêm compatibilidade histórica)
	if cfg.EdgeOptions.TimeoutSeconds == 0 {
		cfg.EdgeOptions.TimeoutSeconds = 180
	}
	if !cfg.EdgeOptions.AutoProfile && cfg.EdgeOptions.UserDataDir == "" {
		cfg.EdgeOptions.AutoProfile = true
	}
	if cfg.EdgeOptions.RefreshSkewSeconds == 0 {
		cfg.EdgeOptions.RefreshSkewSeconds = 300
	}

	// limpar cache de cookies se pedido
	if *cleanCache {
		if err := cfg.CleanCookieCache(); err != nil && !os.IsNotExist(err) {
			log.Printf("[warn] CleanCookieCache: %v (continuo)", err)
		}
	}

	// --- construir http.Client com timeout ajustável ---
	effectiveTimeout := time.Duration(*httpTimeoutSec) * time.Second
	if effectiveTimeout <= 0 {
		effectiveTimeout = 30 * time.Second
	}

	httpTransport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	spHTTPClient := http.Client{
		Timeout:   effectiveTimeout,
		Transport: httpTransport,
	}

	// --- construir o SP client (gosip) com o nosso http.Client e a nossa estratégia edgeondemand ---
	spClient := &gosip.SPClient{
		Client:     spHTTPClient,
		AuthCnfg:   cfg,
		ConfigPath: *configPath,
	}

	// hooks verbosos se debug activo no private.json,
	// mas desligados se --quiet estiver ligado.
	if cfg.EdgeOptions != nil && cfg.EdgeOptions.Debug && !*quiet {
		spClient.Hooks = &gosip.HookHandlers{
			OnRequest: func(ev *gosip.HookEvent) {
				log.Printf("[sp][req] %s %s", ev.Request.Method, safeURL(ev.Request.URL))
			},
			OnResponse: func(ev *gosip.HookEvent) {
				log.Printf("[sp][res] %d in %s %s",
					ev.StatusCode,
					ev.Duration.Round(time.Millisecond),
					safeURL(ev.Request.URL),
				)
			},
			OnRetry: func(ev *gosip.HookEvent) {
				log.Printf("[sp][retry] status=%d err=%v url=%s",
					ev.StatusCode,
					ev.Error,
					safeURL(ev.Request.URL),
				)
			},
			OnError: func(ev *gosip.HookEvent) {
				log.Printf("[sp][error] status=%d err=%v url=%s",
					ev.StatusCode,
					ev.Error,
					safeURL(ev.Request.URL),
				)
			},
		}
	}

	svc := spapi.New(spClient)

	// timeout "externo" (protege a chamada toda) — configurável via --global-timeout
	var ctx context.Context
	var cancel context.CancelFunc
	if *globalTimeoutSec > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(*globalTimeoutSec)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	// --- executar modo solicitado ---
	switch *mode {

	case "list-items":
		handleListItems(
			ctx,
			svc,
			*listName,
			*selectFields,
			resolvedFilter,
			resolvedOrderBy,
			*top,
			resolvedLatest,
			*allFlag,
			*cleanOutput,
			*outputFmt,
			*summaryFlag,
		)

	case "latest-item":
		handleLatestItem(
			ctx,
			svc,
			*listName,
			*selectFields,
			resolvedFilter,
			resolvedOrderBy,
			*cleanOutput,
			*outputFmt,
			*summaryFlag,
		)

	case "get-item":
		handleGetItem(ctx, svc, *listName, *idFlag, *selectFields, *cleanOutput, *outputFmt)

	case "add-item":
		fieldsMap, err := parseFieldsKV(*fieldsFlag)
		if err != nil {
			fatalJSON("add-item", err)
		}
		handleAddItem(ctx, svc, *listName, fieldsMap, *selectFields, *cleanOutput, *outputFmt)

	case "update-item":
		fieldsMap, err := parseFieldsKV(*fieldsFlag)
		if err != nil {
			fatalJSON("update-item", err)
		}
		handleUpdateItem(ctx, svc, *listName, *idFlag, fieldsMap, *selectFields, *cleanOutput, *outputFmt)

	case "delete-item":
		handleDeleteItem(ctx, svc, *listName, *idFlag)

	default:
		fatalJSON("init", fmt.Errorf("modo desconhecido: %s", *mode))
	}
}

// firstNonEmpty devolve o primeiro string não-vazio.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// safeURL remove a querystring para evitar tokens sensíveis nos logs de debug.
func safeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	cp := *u
	cp.RawQuery = ""
	return cp.String()
}

// cliError é o envelope consistente para erros fatais.
type cliError struct {
	OK    bool   `json:"ok"`
	Mode  string `json:"mode"`
	Error string `json:"error"`
}

// fatalJSON escreve um erro em JSON "bonito" no stdout e sai com código 1.
func fatalJSON(mode string, err error) {
	ce := cliError{
		OK:    false,
		Mode:  mode,
		Error: fmt.Sprintf("%s falhou: %v", mode, err),
	}
	data, _ := json.MarshalIndent(ce, "", "  ")
	fmt.Fprintln(os.Stdout, string(data))
	os.Exit(1)
}

// =====================
// Implementações dos modos
// =====================

func handleListItems(
	ctx context.Context,
	svc *spapi.SPService,
	listName string,
	selectFields string,
	filter string,
	orderBy string,
	top int,
	latestOnly bool,
	all bool,
	cleanOutput bool,
	outputFmt string,
	summary bool,
) {
	if listName == "" {
		fatalJSON("list-items", fmt.Errorf("--list é obrigatório"))
	}

	opts := spapi.ListQueryOptions{
		ListName:   listName,
		Select:     selectFields,
		Filter:     filter,
		OrderBy:    orderBy,
		Top:        top,
		LatestOnly: latestOnly,
		All:        all,
	}

	items, stats, err := svc.ListItems(ctx, opts)
	if err != nil {
		fatalJSON("list-items", err)
	}

	items = maybeCleanSlice(items, cleanOutput)

	if err := emitListOutput(items, outputFmt, selectFields, false); err != nil {
		fatalJSON("list-items", err)
	}

	maybePrintSummary(stats, len(items), summary, top, latestOnly, all)
}

func handleLatestItem(
	ctx context.Context,
	svc *spapi.SPService,
	listName string,
	selectFields string,
	filter string,
	orderBy string,
	cleanOutput bool,
	outputFmt string,
	summary bool,
) {
	if listName == "" {
		fatalJSON("latest-item", fmt.Errorf("--list é obrigatório"))
	}

	// latest-item = varrimento completo (--all) + só o mais recente
	opts := spapi.ListQueryOptions{
		ListName:   listName,
		Select:     selectFields,
		Filter:     filter,
		OrderBy:    orderBy,
		Top:        1,
		LatestOnly: true,
		All:        true,
	}

	items, stats, err := svc.ListItems(ctx, opts)
	if err != nil {
		fatalJSON("latest-item", err)
	}

	items = maybeCleanSlice(items, cleanOutput)

	if err := emitListOutput(items, outputFmt, selectFields, true); err != nil {
		fatalJSON("latest-item", err)
	}

	maybePrintSummary(stats, len(items), summary, 1, true, true)
}

func handleGetItem(
	ctx context.Context,
	svc *spapi.SPService,
	listName string,
	id int,
	selectFields string,
	cleanOutput bool,
	outputFmt string,
) {
	if listName == "" {
		fatalJSON("get-item", fmt.Errorf("--list é obrigatório"))
	}
	if id <= 0 {
		fatalJSON("get-item", fmt.Errorf("--id inválido"))
	}

	item, err := svc.GetItemByID(ctx, listName, id, selectFields)
	if err != nil {
		fatalJSON("get-item", err)
	}

	item = maybeCleanMap(item, cleanOutput)

	if err := emitListOutput([]map[string]any{item}, outputFmt, selectFields, true); err != nil {
		fatalJSON("get-item", err)
	}
}

func handleAddItem(
	ctx context.Context,
	svc *spapi.SPService,
	listName string,
	fields map[string]any,
	selectFields string,
	cleanOutput bool,
	outputFmt string,
) {
	if listName == "" {
		fatalJSON("add-item", fmt.Errorf("--list é obrigatório"))
	}
	if len(fields) == 0 {
		fatalJSON("add-item", fmt.Errorf("--fields é obrigatório (ex.: Matricula=57RT01,Operador=1006371)"))
	}

	created, err := svc.AddItem(ctx, listName, fields)
	if err != nil {
		fatalJSON("add-item", err)
	}

	id := extractIDFromMap(created)

	afterWriteFetchAndPrint(
		ctx,
		svc,
		listName,
		id,
		selectFields,
		cleanOutput,
		outputFmt,
		created, // fallback se o GET falhar
		"add-item",
	)
}

func handleUpdateItem(
	ctx context.Context,
	svc *spapi.SPService,
	listName string,
	id int,
	fields map[string]any,
	selectFields string,
	cleanOutput bool,
	outputFmt string,
) {
	if listName == "" {
		fatalJSON("update-item", fmt.Errorf("--list é obrigatório"))
	}
	if id <= 0 {
		fatalJSON("update-item", fmt.Errorf("--id inválido"))
	}
	if len(fields) == 0 {
		fatalJSON("update-item", fmt.Errorf("--fields é obrigatório (ex.: Operador=999999)"))
	}

	_, err := svc.UpdateItem(ctx, listName, id, fields)
	if err != nil {
		fatalJSON("update-item", err)
	}

	afterWriteFetchAndPrint(
		ctx,
		svc,
		listName,
		id,
		selectFields,
		cleanOutput,
		outputFmt,
		nil, // sem fallback útil aqui
		"update-item",
	)
}

func handleDeleteItem(
	ctx context.Context,
	svc *spapi.SPService,
	listName string,
	id int,
) {
	if listName == "" {
		fatalJSON("delete-item", fmt.Errorf("--list é obrigatório"))
	}
	if id <= 0 {
		fatalJSON("delete-item", fmt.Errorf("--id inválido"))
	}

	if err := svc.DeleteItem(ctx, listName, id); err != nil {
		fatalJSON("delete-item", err)
	}

	printJSON(map[string]any{
		"deleted": true,
		"id":      id,
	})
}

// afterWriteFetchAndPrint:
// usado por add-item / update-item.
// tenta ler o item final do SharePoint (respeitando --select),
// aplica --clean-output e imprime no formato pedido.
// fallbackItem é usado no caso de add-item se o GET falhar.
func afterWriteFetchAndPrint(
	ctx context.Context,
	svc *spapi.SPService,
	listName string,
	id int,
	selectFields string,
	cleanOutput bool,
	outputFmt string,
	fallbackItem map[string]any,
	mode string,
) {
	var item map[string]any

	if id > 0 {
		fetched, err := svc.GetItemByID(ctx, listName, id, selectFields)
		if err == nil {
			item = fetched
		} else if fallbackItem != nil {
			item = fallbackItem
		} else {
			item = map[string]any{}
		}
	} else {
		if fallbackItem != nil {
			item = fallbackItem
		} else {
			item = map[string]any{}
		}
	}

	item = maybeCleanMap(item, cleanOutput)

	if err := emitListOutput([]map[string]any{item}, outputFmt, selectFields, true); err != nil {
		fatalJSON(mode, fmt.Errorf("output falhou: %w", err))
	}
}

// =====================
// Helpers de output / parsing / resumo
// =====================

func maybePrintSummary(
	stats spapi.QueryStats,
	count int,
	summary bool,
	top int,
	latestOnly bool,
	all bool,
) {
	if !summary {
		return
	}

	// Heurística "cumpriu o pedido?"
	// - latestOnly: basta ter >=1.
	// - com top>0: se devolveu >= top.
	// - sem top/sem latest:
	//     se não parámos cedo por timeout/throttle (StoppedEarly=false),
	//     então consideramos satisfeito.
	topSatisfied := false
	switch {
	case latestOnly:
		topSatisfied = count >= 1
	case top > 0:
		topSatisfied = count >= top
	default:
		// sem top e sem latest:
		// em --all ou sem --all é a mesma decisão:
		topSatisfied = !stats.StoppedEarly
	}

	log.Printf(
		"[summary] items=%d pages=%d throttled=%t partial=%t fallback=%t stoppedEarly=%t topSatisfied=%t",
		count,
		stats.PagesFetched,
		stats.Throttled,
		stats.Partial,
		stats.UsedFallback,
		stats.StoppedEarly,
		topSatisfied,
	)
}

// emitListOutput escreve os items em stdout no formato pedido.
// singleIfPossible=true → se houver só 1 item e formato json, devolve objeto sozinho (não array).
func emitListOutput(items []map[string]any, format string, selectFields string, singleIfPossible bool) error {
	switch strings.ToLower(format) {
	case "json":
		if singleIfPossible && len(items) == 1 {
			data, err := json.MarshalIndent(items[0], "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}
		data, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil

	case "jsonl":
		for _, it := range items {
			data, err := json.Marshal(it)
			if err != nil {
				return err
			}
			fmt.Println(string(data))
		}
		return nil

	case "csv":
		return emitCSV(items, selectFields)

	default:
		data, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
}

func emitCSV(items []map[string]any, selectFields string) error {
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()

	if len(items) == 0 {
		return nil
	}

	cols := inferCSVColumns(items, selectFields)

	if err := w.Write(cols); err != nil {
		return err
	}

	for _, it := range items {
		row := make([]string, len(cols))
		for i, col := range cols {
			if v, ok := it[col]; ok {
				row[i] = fmt.Sprint(v)
			} else {
				row[i] = ""
			}
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

// inferCSVColumns:
// 1. se o utilizador passou --select, usamos essa ordem.
// 2. caso contrário, usamos as chaves do primeiro item ordenadas alfabeticamente
func inferCSVColumns(items []map[string]any, selectFields string) []string {
	seen := map[string]bool{}
	var cols []string

	sf := strings.TrimSpace(selectFields)
	if sf != "" {
		parts := strings.Split(sf, ",")
		for _, p := range parts {
			col := strings.TrimSpace(p)
			if col == "" {
				continue
			}
			if !seen[col] {
				seen[col] = true
				cols = append(cols, col)
			}
		}
		if len(cols) > 0 {
			return cols
		}
	}

	if len(items) == 0 {
		return []string{}
	}

	for k := range items[0] {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols
}

// parseFieldsKV converte "A=B,C=D,E=F" → map[string]any{"A":"B",...}
func parseFieldsKV(s string) (map[string]any, error) {
	out := map[string]any{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}

	pairs := strings.Split(s, ",")
	for _, p := range pairs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("par inválido em --fields: %q (esperado key=value)", p)
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if key == "" {
			return nil, fmt.Errorf("chave vazia em --fields")
		}
		out[key] = val
	}
	return out, nil
}

// maybeCleanSlice remove chaves "__..." se clean==true
func maybeCleanSlice(items []map[string]any, clean bool) []map[string]any {
	if !clean {
		return items
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, cleanMapInternal(it))
	}
	return out
}

// maybeCleanMap remove chaves "__..." num único map
func maybeCleanMap(m map[string]any, clean bool) map[string]any {
	if !clean {
		return m
	}
	return cleanMapInternal(m)
}

// cleanMapInternal copia o map sem as chaves internas tipo "__metadata"
func cleanMapInternal(m map[string]any) map[string]any {
	cleaned := make(map[string]any, len(m))
	for k, v := range m {
		if strings.HasPrefix(k, "__") {
			continue
		}
		cleaned[k] = v
	}
	return cleaned
}

// printJSON envia JSON "bonito" para stdout.
func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fallback := map[string]any{
			"ok":    false,
			"mode":  "print",
			"error": fmt.Sprintf("json marshal failed: %v", err),
		}
		data2, _ := json.MarshalIndent(fallback, "", "  ")
		fmt.Fprintln(os.Stdout, string(data2))
		return
	}
	fmt.Println(string(data))
}

// extractIDFromMap tenta ler "ID"/"Id"/"id" e devolve int.
// SharePoint às vezes devolve "ID" e "Id" duplicado. Mantemos ambos
// por compatibilidade e para não partir nada do lado do consumidor.
func extractIDFromMap(m map[string]any) int {
	keys := []string{"ID", "Id", "id"}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch vv := v.(type) {
			case int:
				return vv
			case int32:
				return int(vv)
			case int64:
				return int(vv)
			case float32:
				return int(vv)
			case float64:
				return int(vv)
			case json.Number:
				i, _ := vv.Int64()
				return int(i)
			}
		}
	}
	return 0
}
