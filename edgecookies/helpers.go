package edgecookies

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chromedp/cdproto/network"
)

// ensureEdgePath tenta descobrir o executável do Edge, caso não seja fornecido.
func ensureEdgePath(p string) (string, error) {
	if p != "" {
		return p, nil
	}

	switch runtime.GOOS {
	case "windows":
		candidates := []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Microsoft", "Edge SxS", "Application", "msedge.exe"), // Canary
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		if lp, err := exec.LookPath("msedge.exe"); err == nil {
			return lp, nil
		}
		return "", fmt.Errorf("msedge.exe não encontrado")

	case "darwin":
		alts := []string{
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Microsoft Edge Beta.app/Contents/MacOS/Microsoft Edge Beta",
			"/Applications/Microsoft Edge Dev.app/Contents/MacOS/Microsoft Edge Dev",
			"/Applications/Microsoft Edge Canary.app/Contents/MacOS/Microsoft Edge Canary",
		}
		for _, a := range alts {
			if _, err := os.Stat(a); err == nil {
				return a, nil
			}
		}
		return "", fmt.Errorf("Microsoft Edge.app não encontrado")

	default: // linux / *nix
		names := []string{
			"microsoft-edge",
			"microsoft-edge-stable",
			"microsoft-edge-dev",
			"microsoft-edge-beta",
		}
		for _, n := range names {
			if lp, err := exec.LookPath(n); err == nil {
				return lp, nil
			}
		}
		return "", fmt.Errorf("microsoft-edge não encontrado no PATH")
	}
}

// urlParseHost garante que conseguimos fazer parse ao URL mesmo que venha sem esquema.
func urlParseHost(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		// se chamarem "sharepoint.contoso.com/sites/x" sem https://
		u.Scheme = "https"
	}
	return u, nil
}

// hostOf devolve o host (domínio) em lowercase, ou "" se não conseguir.
func hostOf(raw string) string {
	u, err := urlParseHost(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// headersGetAll devolve todos os valores de uma header HTTP (case-insensitive).
// Isto é útil para debug "Set-Cookie".
func headersGetAll(h network.Headers, key string) []string {
	var out []string
	want := strings.ToLower(key)
	for k, v := range h {
		if strings.ToLower(k) != want {
			continue
		}
		switch vv := v.(type) {
		case string:
			out = append(out, vv)
		case []string:
			out = append(out, vv...)
		case []interface{}:
			for _, iv := range vv {
				if s, ok := iv.(string); ok {
					out = append(out, s)
				}
			}
		default:
			out = append(out, fmt.Sprintf("%v", v))
		}
	}
	return out
}
