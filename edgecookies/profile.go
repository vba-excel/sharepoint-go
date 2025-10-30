// edgecookies/profile.go
package edgecookies

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type ProfileGuess struct {
	UserDataDir string
	ProfileDir  string
}

// DetectDefaultUserProfile descobre o diretório do perfil real e o “last_used” (ex.: "Default").
func DetectDefaultUserProfile() (*ProfileGuess, error) {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = filepath.Join(os.Getenv("LocalAppData"), "Microsoft", "Edge", "User Data")
	case "darwin":
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Library", "Application Support", "Microsoft Edge")
	default: // linux
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config", "microsoft-edge")
		if _, err := os.Stat(base); err != nil {
			base = filepath.Join(home, ".config", "microsoft-edge-stable")
		}
	}
	if base == "" {
		return nil, fmt.Errorf("não foi possível determinar o diretório base do Edge")
	}
	if _, err := os.Stat(base); err != nil {
		return nil, fmt.Errorf("edge user data não encontrado em %s", base)
	}

	localState := filepath.Join(base, "Local State")
	profileDir := "Default"
	if b, err := os.ReadFile(localState); err == nil {
		var ls struct {
			Profile struct {
				LastUsed string `json:"last_used"`
			} `json:"profile"`
		}
		if json.Unmarshal(b, &ls) == nil && ls.Profile.LastUsed != "" {
			profileDir = ls.Profile.LastUsed
		}
	}
	return &ProfileGuess{UserDataDir: base, ProfileDir: profileDir}, nil
}

// IsUserDataDirLocked devolve true se o diretório do perfil aparenta estar em uso.
// Cobre variantes do Chromium: lockfile/LOCK e Singleton* (Windows/macOS/Linux).
func IsUserDataDirLocked(userDataDir string) bool {
	if userDataDir == "" {
		return false
	}
	// Candidatos conhecidos (dependem de SO / versão do Chromium)
	candidates := []string{
		filepath.Join(userDataDir, "lockfile"),        // Windows (Edge/Chrome)
		filepath.Join(userDataDir, "LOCK"),            // macOS/Linux (por vezes também no Win)
		filepath.Join(userDataDir, "SingletonLock"),   // Chromium
		filepath.Join(userDataDir, "SingletonCookie"), // Chromium
		filepath.Join(userDataDir, "SingletonSocket"), // Chromium
		// perfis típicos:
		filepath.Join(userDataDir, "Default", "LOCK"),
	}

	// LOCK em perfis "Profile X"
	if matches, _ := filepath.Glob(filepath.Join(userDataDir, "Profile *", "LOCK")); len(matches) > 0 {
		candidates = append(candidates, matches...)
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

