package edgecookies

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type Options struct {
	EdgePath    string        // Caminho para msedge (opcional; autodetect se vazio)
	UserDataDir string        // Diretório do perfil; se vazio cria um temporário e apaga no fim
	ProfileDir  string        // Nome do perfil (ex.: "Default", "Profile 1")
	Headless    bool          // true=headless; false=janela visível
	Timeout     time.Duration // Timeout total (default 60s)
	Debug       bool          // Logs verbosos para stderr
	WaitFor     time.Duration // (só em GetCookie) aguardar pela cookie até esta duração
}

// ListCookies: arranca Edge, navega para u e devolve todas as cookies (inclui HttpOnly).
func ListCookies(u string, opt Options) ([]*network.Cookie, error) {
	if u == "" {
		return nil, fmt.Errorf("url vazio")
	}

	// logger simples (stderr) para marcadores do fluxo
	dbg := func(string, ...any) {}
	if opt.Debug {
		dbg = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[edgecookies] "+f+"\n", a...) }
	}

	edgeExe, err := ensureEdgePath(opt.EdgePath)
	if err != nil {
		return nil, err
	}
	dbg("Edge executable: %s", edgeExe)

	// Perfil isolado por omissão (ou o fornecido)
	userDataDir := opt.UserDataDir
	tempProfile := false
	if userDataDir == "" {
		userDataDir, err = os.MkdirTemp("", "edge-profile-*")
		if err != nil {
			return nil, fmt.Errorf("criar perfil temporário: %w", err)
		}
		tempProfile = true
		dbg("Created temp UserDataDir: %s", userDataDir)
		defer func() {
			dbg("Cleaning temp UserDataDir")
			_ = os.RemoveAll(userDataDir)
		}()
	} else {
		dbg("Using provided UserDataDir: %s (profile-dir: %s)", userDataDir, opt.ProfileDir)
	}

	if opt.Timeout <= 0 {
		opt.Timeout = 60 * time.Second
	}

	allocOpts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(edgeExe),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(userDataDir),

		// Garantir headless “novo” (evita abrir janela no Chrome/Edge recentes)
		chromedp.Flag("headless", opt.Headless),
		chromedp.Flag("headless=new", opt.Headless),
		chromedp.Flag("disable-gpu", opt.Headless),

		chromedp.Flag("start-maximized", !opt.Headless),
	}
	if opt.ProfileDir != "" && !tempProfile {
		allocOpts = append(allocOpts, chromedp.Flag("profile-directory", opt.ProfileDir))
	}
	dbg("Starting Edge (headless=%v) ...", opt.Headless)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer cancelAlloc()

	// Contexto/tab único
	ctxOpts := []chromedp.ContextOption{}
	if opt.Debug {
		// logs CDP verbosos para stderr (não poluem stdout/JSON)
		ctxOpts = append(ctxOpts,
			chromedp.WithLogf(func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }),
			chromedp.WithDebugf(func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }),
			chromedp.WithErrorf(func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }),
		)
	}
	ctx, cancelTab := chromedp.NewContext(allocCtx, ctxOpts...)
	defer cancelTab()

	// Timeout total
	ctx, cancelTO := context.WithTimeout(ctx, opt.Timeout)
	defer cancelTO()

	// Listeners de debug (requests, responses, set-cookie, loading)
	if opt.Debug {
		chromedp.ListenTarget(ctx, func(ev interface{}) {
			switch e := ev.(type) {
			case *network.EventRequestWillBeSent:
				fmt.Fprintf(os.Stderr, "[CDP][REQ] %s %s (id=%s)\n", e.Request.Method, e.Request.URL, e.RequestID)
				if e.RedirectResponse != nil {
					fmt.Fprintf(os.Stderr, "[CDP][REDIRECT] %d %s -> %s\n", int(e.RedirectResponse.Status), e.RedirectResponse.URL, e.Request.URL)
				}
			case *network.EventResponseReceived:
				fmt.Fprintf(os.Stderr, "[CDP][RES] %d %s type=%s (id=%s)\n", int(e.Response.Status), e.Response.URL, e.Type.String(), e.RequestID)
			case *network.EventResponseReceivedExtraInfo:
				for _, v := range headersGetAll(e.Headers, "set-cookie") {
					fmt.Fprintf(os.Stderr, "[CDP][SET-COOKIE] %s\n", v)
				}
			case *network.EventLoadingFinished:
				fmt.Fprintf(os.Stderr, "[CDP][DONE] req=%s encoded=%.0f\n", e.RequestID, e.EncodedDataLength)
			case *network.EventLoadingFailed:
				fmt.Fprintf(os.Stderr, "[CDP][FAIL] req=%s error=%s canceled=%v\n", e.RequestID, e.ErrorText, e.Canceled)
			}
		})
	}

	dbg("Enabling Network domain")
	// Ativar Network antes de navegar
	if err := chromedp.Run(ctx, network.Enable()); err != nil {
		return nil, fmt.Errorf("ativar network: %w", err)
	}
	if !opt.Headless {
		_ = chromedp.Run(ctx, page.BringToFront()) // best-effort
	}

	var cookies []*network.Cookie
	dbg("Navigating to %s", u)

	// Navegar, esperar e recolher cookies
	err = chromedp.Run(ctx,
		chromedp.Navigate(u),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// preferir cookies associadas ao URL (mais limpo, já filtrado pelo DevTools)
			cs, err := network.GetCookies().WithURLs([]string{u}).Do(ctx)
			if err == nil && len(cs) > 0 {
				cookies = cs
				return nil
			}

			// fallback: pedir TODAS e filtrar pelo domínio
			cs, err2 := network.GetCookies().Do(ctx)
			if err2 != nil {
				if err != nil {
					return err
				}
				return err2
			}
			h := strings.ToLower(hostOf(u))
			filtered := make([]*network.Cookie, 0, len(cs))
			for _, c := range cs {
				d := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
				if d == h || strings.HasSuffix(h, "."+d) {
					filtered = append(filtered, c)
				}
			}
			cookies = filtered
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}

	dbg("Got %d cookies", len(cookies))
	return cookies, nil
}

// GetCookie: igual a ListCookies mas devolve só uma cookie específica.
// Se opt.WaitFor > 0, faz polling durante esse tempo à procura da cookie.
func GetCookie(u, name string, opt Options) (*network.Cookie, error) {
	if name == "" {
		return nil, fmt.Errorf("name vazio")
	}

	dbg := func(string, ...any) {}
	if opt.Debug {
		dbg = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[edgecookies] "+f+"\n", a...) }
	}

	if opt.WaitFor <= 0 {
		dbg("One-shot get cookie %q", name)

		cs, err := ListCookies(u, opt)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			if c.Name == name {
				return c, nil
			}
		}
		return nil, fmt.Errorf("cookie %q não encontrada", name)
	}

	// Polling: repetir ListCookies até encontrar ou expirar
	deadline := time.Now().Add(opt.WaitFor)
	var lastErr error

	dbg("Polling for cookie %q up to %s", name, opt.WaitFor)

	for time.Now().Before(deadline) {
		cs, err := ListCookies(u, opt)
		if err == nil {
			for _, c := range cs {
				if c.Name == name {
					dbg("Cookie %q found", name)
					return c, nil
				}
			}
		} else {
			lastErr = err
			dbg("ListCookies returned error (will retry): %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("cookie %q não encontrada dentro de %s", name, opt.WaitFor)
}
