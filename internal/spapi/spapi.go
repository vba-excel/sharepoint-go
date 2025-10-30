package spapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/koltyakov/gosip"
	"github.com/koltyakov/gosip/api"
)

// SPService é o nosso "facade" sobre gosip/api.
type SPService struct {
	sp *api.SP
}

// New cria o serviço a partir de um gosip.SPClient autenticado.
func New(spClient *gosip.SPClient) *SPService {
	return &SPService{
		sp: api.NewSP(spClient),
	}
}

// ctxAlive devolve um erro se o contexto já tiver sido cancelado/expirado.
func ctxAlive(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// listHandle decide como aceder à lista (por título vs caminho absoluto/GUID).
func (s *SPService) listHandle(listName string) *api.List {
	trimmed := strings.TrimSpace(listName)

	if trimmed == "" {
		return s.sp.Web().GetList(trimmed)
	}

	if strings.HasPrefix(trimmed, "/") ||
		strings.Contains(trimmed, "/") ||
		strings.HasPrefix(trimmed, "{") {
		return s.sp.Web().GetList(trimmed)
	}

	return s.sp.Web().Lists().GetByTitle(trimmed)
}

// ===== modelos de query =====

type ListQueryOptions struct {
	ListName   string
	Select     string
	Filter     string
	OrderBy    string
	Top        int  // se >0 e All=true, atua como limite TOTAL de itens devolvidos
	LatestOnly bool // devolve só o item com maior ID (após recolha)
	All        bool // se true, percorre todas as páginas ($skiptoken)
}

// QueryStats guarda métricas internas sobre a query.
type QueryStats struct {
	PagesFetched int  // nº total de pedidos REST que fizemos (inclui tentativas / páginas)
	Throttled    bool // true se apanhámos SPQueryThrottledException em algum momento
	Partial      bool // true se NÃO percorremos tudo até ao fim natural
	StoppedEarly bool // true se parámos "à força" (timeout/throttle a meio/etc.)
	UsedFallback bool // true se tivemos de usar o fallback local (filtrar em memória)
}

// ListItems é a função pública chamada pelo CLI.
func (s *SPService) ListItems(ctx context.Context, opts ListQueryOptions) ([]map[string]any, QueryStats, error) {
	if opts.ListName == "" {
		return nil, QueryStats{}, fmt.Errorf("ListItems: ListName vazio")
	}
	if err := ctxAlive(ctx); err != nil {
		return nil, QueryStats{}, err
	}

	// Caminho --all (paginação completa / varrimento global)
	if opts.All {
		itemsAll, stats, err := s.listItemsAllPaginated(ctx, opts)
		if err != nil {
			return nil, stats, err
		}
		return applyLatestOnly(itemsAll, opts.LatestOnly), stats, nil
	}

	// Caminho simples (1 página + fallback local se throttling)
	if err := ctxAlive(ctx); err != nil {
		return nil, QueryStats{}, err
	}

	// 1ª tentativa: query normal com Filter, OrderBy, Top
	itemsNormal, err := s.execListQueryOnce(opts)
	if err == nil {
		stats := QueryStats{
			PagesFetched: 1,
			Throttled:    false,
			Partial:      false,
			StoppedEarly: false,
			UsedFallback: false,
		}
		return applyLatestOnly(itemsNormal, opts.LatestOnly), stats, nil
	}

	// Se não foi throttling → erro "real" e acabou.
	if !isThrottled(err) {
		stats := QueryStats{
			PagesFetched: 1,
			Throttled:    false,
			Partial:      false,
			StoppedEarly: false,
			UsedFallback: false,
		}
		return nil, stats, err
	}

	// Foi throttling → tentamos fallback cliente
	fieldName, wantedVal, ok := parseSimpleEqFilter(opts.Filter)
	if !ok {
		// filtro complexo; não conseguimos fallback local
		stats := QueryStats{
			PagesFetched: 1,
			Throttled:    true,
			Partial:      false,
			StoppedEarly: false,
			UsedFallback: false,
		}
		return nil, stats, err
	}

	// contexto já cancelado?
	if cerr := ctxAlive(ctx); cerr != nil {
		stats := QueryStats{
			PagesFetched: 1,
			Throttled:    true,
			Partial:      true,
			StoppedEarly: true,
			UsedFallback: false,
		}
		return nil, stats, cerr
	}

	// fallback: pedir chunk grande sem filtro no servidor
	fbOpts := opts
	fbOpts.Filter = ""

	fallbackTop := fbOpts.Top
	if fallbackTop <= 0 {
		fallbackTop = 50
	}
	if fallbackTop < 200 {
		fallbackTop = 200
	}
	fbOpts.Top = fallbackTop

	itemsFallback, err2 := s.execListQueryOnce(fbOpts)

	stats := QueryStats{
		PagesFetched: 2,
		Throttled:    true,
		Partial:      false,
		StoppedEarly: false,
		UsedFallback: true,
	}

	if err2 != nil {
		// fallback falhou → devolvemos erro original de throttling
		return nil, stats, err
	}

	// Outra verificação de contexto após fallback
	if cerr := ctxAlive(ctx); cerr != nil {
		stats.Partial = true
		stats.StoppedEarly = true
		return nil, stats, cerr
	}

	filteredLocal := localFilterEq(itemsFallback, fieldName, wantedVal)

	if opts.Top > 0 && len(filteredLocal) > opts.Top {
		filteredLocal = filteredLocal[:opts.Top]
	}

	return applyLatestOnly(filteredLocal, opts.LatestOnly), stats, nil
}

// listItemsAllPaginated trata especificamente o caso --all.
func (s *SPService) listItemsAllPaginated(ctx context.Context, opts ListQueryOptions) ([]map[string]any, QueryStats, error) {
	if err := ctxAlive(ctx); err != nil {
		return nil, QueryStats{}, err
	}

	// 1ª tentativa: paginação normal COM filter/orderby/top.
	itemsAll, pages, throttled, partial, stoppedEarly, err := s.execListQueryAllPages(ctx, opts)
	if err == nil {
		stats := QueryStats{
			PagesFetched: pages,
			Throttled:    throttled,
			Partial:      partial,
			StoppedEarly: stoppedEarly,
			UsedFallback: false,
		}
		return itemsAll, stats, nil
	}

	// erro real que não é throttle? devolvemos já.
	if !isThrottled(err) {
		stats := QueryStats{
			PagesFetched: pages,
			Throttled:    throttled,
			Partial:      partial,
			StoppedEarly: stoppedEarly,
			UsedFallback: false,
		}
		return nil, stats, err
	}

	// throttle logo na primeira página → tentar fallback local
	fieldName, wantedVal, ok := parseSimpleEqFilter(opts.Filter)
	if !ok {
		stats := QueryStats{
			PagesFetched: pages,
			Throttled:    true,
			Partial:      partial,
			StoppedEarly: stoppedEarly,
			UsedFallback: false,
		}
		return nil, stats, err
	}

	if err := ctxAlive(ctx); err != nil {
		stats := QueryStats{
			PagesFetched: pages,
			Throttled:    true,
			Partial:      true,
			StoppedEarly: true,
			UsedFallback: false,
		}
		return nil, stats, err
	}

	fbOpts := opts
	fbOpts.Filter = ""

	filteredAll, pages2, throttled2, partial2, stoppedEarly2, err2 := s.execListQueryAllPagesLocalFilter(ctx, fbOpts, fieldName, wantedVal)
	aggStats := QueryStats{
		PagesFetched: pages + pages2,
		Throttled:    true || throttled2,
		Partial:      partial || partial2,
		StoppedEarly: stoppedEarly || stoppedEarly2,
		UsedFallback: true,
	}

	if err2 != nil {
		return nil, aggStats, err2
	}

	return filteredAll, aggStats, nil
}

// execListQueryOnce faz UMA chamada REST usando gosip/api.
func (s *SPService) execListQueryOnce(opts ListQueryOptions) ([]map[string]any, error) {
	items := s.listHandle(opts.ListName).Items()

	if opts.Select != "" {
		items = items.Select(opts.Select)
	}
	if opts.Filter != "" {
		items = items.Filter(opts.Filter)
	}
	if opts.OrderBy != "" {
		field, asc := parseOrderBy(opts.OrderBy)
		if field != "" {
			items = items.OrderBy(field, asc)
		}
	}
	if opts.Top > 0 {
		items = items.Top(opts.Top)
	}

	resp, err := items.Get()
	if err != nil {
		return nil, err
	}

	rawList := resp.ToMap()
	if len(rawList) == 0 {
		return []map[string]any{}, nil
	}
	return rawList, nil
}

// execListQueryAllPages percorre todas as páginas ($skiptoken) até esgotar
// ou até atingir opts.Top TOTAL (se >0).
//
// devolve também:
//  - partial      = true se NÃO percorremos tudo até ao fim natural
//  - stoppedEarly = true se parámos por timeout / throttle / cancel
func (s *SPService) execListQueryAllPages(
	ctx context.Context,
	opts ListQueryOptions,
) ([]map[string]any, int, bool, bool, bool, error) {

	items := s.listHandle(opts.ListName).Items()

	if opts.Select != "" {
		items = items.Select(opts.Select)
	}
	if opts.Filter != "" {
		items = items.Filter(opts.Filter)
	}
	if opts.OrderBy != "" {
		field, asc := parseOrderBy(opts.OrderBy)
		if field != "" {
			items = items.OrderBy(field, asc)
		}
	}

	// pageSize gordo para minimizar chamadas
	pageSize := 200
	items = items.Top(pageSize)

	pagesFetched := 0
	throttledEver := false
	partial := false
	stoppedEarly := false

	allMaps := make([]map[string]any, 0, pageSize*4)

	appendPage := func(p *api.ItemsPage) {
		if p == nil {
			return
		}
		chunk := p.Items.ToMap()
		if len(chunk) > 0 {
			allMaps = append(allMaps, chunk...)
		}
	}

	// primeira página
	page, err := items.GetPaged()
	pagesFetched++
	if err != nil {
		if isThrottled(err) {
			throttledEver = true
			partial = true
			stoppedEarly = true
		}
		return nil, pagesFetched, throttledEver, partial, stoppedEarly, err
	}

	appendPage(page)

	totalLimit := opts.Top
	if totalLimit > 0 && len(allMaps) >= totalLimit {
		if len(allMaps) > totalLimit {
			allMaps = allMaps[:totalLimit]
		}
		// parámos cedo mas foi "porque já tínhamos o suficiente"
		partial = false
		stoppedEarly = false
		return allMaps, pagesFetched, throttledEver, partial, stoppedEarly, nil
	}

	for page.HasNextPage() {
		// contexto expirou/cancelou? paramos e devolvemos parcial
		if err := ctxAlive(ctx); err != nil {
			partial = true
			stoppedEarly = true
			break
		}

		page, err = page.GetNextPage()
		pagesFetched++
		if err != nil {
			// throttle a meio → devolvemos parcial sem erro
			if isThrottled(err) {
				throttledEver = true
				partial = true
				stoppedEarly = true
				break
			}
			// erro real → devolvemos erro
			return nil, pagesFetched, throttledEver, partial, stoppedEarly, err
		}

		appendPage(page)

		if totalLimit > 0 && len(allMaps) >= totalLimit {
			if len(allMaps) > totalLimit {
				allMaps = allMaps[:totalLimit]
			}
			// parámos porque já excedia o top pedido ⇒ não é erro
			partial = false
			stoppedEarly = false
			break
		}
	}

	return allMaps, pagesFetched, throttledEver, partial, stoppedEarly, nil
}

// execListQueryAllPagesLocalFilter percorre TODAS as páginas sem Filter server-side
// e faz o filtro em memória: fieldName eq wantedVal.
//
// devolve também partial/stoppedEarly com a mesma semântica que execListQueryAllPages.
func (s *SPService) execListQueryAllPagesLocalFilter(
	ctx context.Context,
	opts ListQueryOptions,
	fieldName, wantedVal string,
) ([]map[string]any, int, bool, bool, bool, error) {

	items := s.listHandle(opts.ListName).Items()

	if opts.Select != "" {
		items = items.Select(opts.Select)
	}
	// opts.Filter já foi limpo pelo caller (fbOpts.Filter = "")
	if opts.OrderBy != "" {
		field, asc := parseOrderBy(opts.OrderBy)
		if field != "" {
			items = items.OrderBy(field, asc)
		}
	}

	pageSize := 200
	items = items.Top(pageSize)

	latestOnly := opts.LatestOnly
	totalLimit := opts.Top

	var best map[string]any
	bestID := 0

	results := make([]map[string]any, 0, pageSize)
	collected := 0

	pagesFetched := 0
	throttledEver := false
	partial := false
	stoppedEarly := false

	processChunk := func(chunk []map[string]any) {
		for _, it := range chunk {
			v, ok := it[fieldName]
			if !ok {
				continue
			}
			if fmt.Sprint(v) != wantedVal {
				continue
			}

			if latestOnly {
				id := extractID(it)
				if best == nil || id > bestID {
					best = it
					bestID = id
				}
			} else {
				results = append(results, it)
				collected++
			}
		}
	}

	// primeira página
	page, err := items.GetPaged()
	pagesFetched++
	if err != nil {
		// throttle logo na primeira -> devolvemos parcial vazio como sucesso
		if isThrottled(err) {
			throttledEver = true
			partial = true
			stoppedEarly = true

			if latestOnly {
				if best == nil {
					return []map[string]any{}, pagesFetched, throttledEver, partial, stoppedEarly, nil
				}
				return []map[string]any{best}, pagesFetched, throttledEver, partial, stoppedEarly, nil
			}
			return results, pagesFetched, throttledEver, partial, stoppedEarly, nil
		}
		// erro real
		return nil, pagesFetched, throttledEver, partial, stoppedEarly, err
	}

	processChunk(page.Items.ToMap())

	done := false
	if totalLimit > 0 && !latestOnly && collected >= totalLimit {
		done = true
		partial = false
		stoppedEarly = false
	}

	for !done && page.HasNextPage() {
		// contexto expirou/cancelou? paramos e devolvemos parcial
		if err := ctxAlive(ctx); err != nil {
			partial = true
			stoppedEarly = true
			break
		}

		page, err = page.GetNextPage()
		pagesFetched++
		if err != nil {
			// throttle a meio → devolvemos parcial
			if isThrottled(err) {
				throttledEver = true
				partial = true
				stoppedEarly = true
				break
			}
			return nil, pagesFetched, throttledEver, partial, stoppedEarly, err
		}

		processChunk(page.Items.ToMap())

		if totalLimit > 0 && !latestOnly && collected >= totalLimit {
			done = true
			partial = false
			stoppedEarly = false
		}
	}

	if latestOnly {
		if best == nil {
			return []map[string]any{}, pagesFetched, throttledEver, partial, stoppedEarly, nil
		}
		return []map[string]any{best}, pagesFetched, throttledEver, partial, stoppedEarly, nil
	}

	if totalLimit > 0 && len(results) > totalLimit {
		results = results[:totalLimit]
	}
	return results, pagesFetched, throttledEver, partial, stoppedEarly, nil
}

// GetItemByID devolve um item específico.
func (s *SPService) GetItemByID(ctx context.Context, listName string, itemID int, selectFields string) (map[string]any, error) {
	if listName == "" {
		return nil, fmt.Errorf("GetItemByID: listName vazio")
	}
	if itemID <= 0 {
		return nil, fmt.Errorf("GetItemByID: itemID inválido")
	}

	if err := ctxAlive(ctx); err != nil {
		return nil, err
	}

	item := s.listHandle(listName).Items().GetByID(itemID)
	if selectFields != "" {
		item = item.Select(selectFields)
	}

	itemResp, err := item.Get()
	if err != nil {
		return nil, err
	}

	return itemResp.ToMap(), nil
}

// AddItem cria um novo item na lista.
func (s *SPService) AddItem(ctx context.Context, listName string, fields map[string]any) (map[string]any, error) {
	if listName == "" {
		return nil, fmt.Errorf("AddItem: listName vazio")
	}
	if fields == nil {
		fields = map[string]any{}
	}

	if err := ctxAlive(ctx); err != nil {
		return nil, err
	}

	list := s.listHandle(listName)

	entityType, err := list.GetEntityType()
	if err != nil {
		return nil, fmt.Errorf("AddItem: GetEntityType falhou: %w", err)
	}

	bodyMap := map[string]any{
		"__metadata": map[string]any{
			"type": entityType,
		},
	}
	for k, v := range fields {
		bodyMap[k] = v
	}

	bodyJSON, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("AddItem: json.Marshal falhou: %w", err)
	}

	addResp, err := list.Items().Add(bodyJSON)
	if err != nil {
		return nil, err
	}

	return addResp.ToMap(), nil
}

// UpdateItem faz PATCH/MERGE num item existente.
func (s *SPService) UpdateItem(ctx context.Context, listName string, itemID int, fields map[string]any) (map[string]any, error) {
	if listName == "" {
		return nil, fmt.Errorf("UpdateItem: listName vazio")
	}
	if itemID <= 0 {
		return nil, fmt.Errorf("UpdateItem: itemID inválido")
	}
	if fields == nil {
		fields = map[string]any{}
	}

	if err := ctxAlive(ctx); err != nil {
		return nil, err
	}

	list := s.listHandle(listName)

	entityType, err := list.GetEntityType()
	if err != nil {
		return nil, fmt.Errorf("UpdateItem: GetEntityType falhou: %w", err)
	}

	bodyMap := map[string]any{
		"__metadata": map[string]any{
			"type": entityType,
		},
	}
	for k, v := range fields {
		bodyMap[k] = v
	}

	bodyJSON, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("UpdateItem: json.Marshal falhou: %w", err)
	}

	updResp, err := list.Items().GetByID(itemID).Update(bodyJSON)
	if err != nil {
		return nil, err
	}

	return updResp.ToMap(), nil
}

// DeleteItem elimina pelo ID.
func (s *SPService) DeleteItem(ctx context.Context, listName string, itemID int) error {
	if listName == "" {
		return fmt.Errorf("DeleteItem: listName vazio")
	}
	if itemID <= 0 {
		return fmt.Errorf("DeleteItem: itemID inválido")
	}

	if err := ctxAlive(ctx); err != nil {
		return err
	}

	return s.listHandle(listName).
		Items().
		GetByID(itemID).
		Delete()
}

// ===== Helpers internos =====

func isThrottled(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(
		strings.ToLower(err.Error()),
		strings.ToLower("SPQueryThrottledException"),
	)
}

// parseSimpleEqFilter tenta interpretar "Campo eq 'Valor'".
func parseSimpleEqFilter(filter string) (field string, value string, ok bool) {
	f := strings.TrimSpace(filter)
	if f == "" {
		return "", "", false
	}

	lower := strings.ToLower(f)
	idx := strings.Index(lower, " eq ")
	if idx < 0 {
		return "", "", false
	}

	left := strings.TrimSpace(f[:idx])
	right := strings.TrimSpace(f[idx+4:]) // depois de " eq "

	if left == "" || right == "" {
		return "", "", false
	}

	if len(right) >= 2 {
		if (right[0] == '\'' && right[len(right)-1] == '\'') ||
			(right[0] == '"' && right[len(right)-1] == '"') {
			right = right[1 : len(right)-1]
		}
	}

	return left, right, true
}

// localFilterEq aplica o filtro eq localmente (em memória).
func localFilterEq(items []map[string]any, fieldName, wantedVal string) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if v, ok := it[fieldName]; ok {
			if fmt.Sprint(v) == wantedVal {
				out = append(out, it)
			}
		}
	}
	return out
}

// applyLatestOnly devolve só o item com ID/Id mais alto se latestOnly=true.
func applyLatestOnly(items []map[string]any, latestOnly bool) []map[string]any {
	if !latestOnly {
		return items
	}
	if len(items) == 0 {
		return items
	}
	maxIdx := 0
	maxID := extractID(items[0])
	for i := 1; i < len(items); i++ {
		if id := extractID(items[i]); id > maxID {
			maxIdx = i
			maxID = id
		}
	}
	return []map[string]any{items[maxIdx]}
}

// parseOrderBy converte "ID desc" → ("ID", false).
func parseOrderBy(ob string) (field string, asc bool) {
	asc = true
	ob = strings.TrimSpace(ob)
	if ob == "" {
		return "", asc
	}
	parts := strings.Fields(ob)
	field = parts[0]
	if len(parts) > 1 {
		dir := strings.ToLower(parts[1])
		if dir == "desc" {
			asc = false
		}
	}
	return field, asc
}

// extractID tenta ler "ID"/"Id"/"id" e devolve int.
func extractID(m map[string]any) int {
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
			}
		}
	}
	return 0
}
