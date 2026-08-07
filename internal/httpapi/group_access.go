package httpapi

// Секрет подписи сессий доступа. Хранится в data/credentials/panel_secret.json,
// создаётся при первом обращении. Потеря файла = разлогин всех сессий.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"standings-edu/internal/fileutil"
)

// panelSecret возвращает секрет подписи сессий. Хранится в
// data/credentials/panel_secret.json; при первом обращении создаётся (32
// случайных байта). Потеря файла = разлогин всех сессий, не более.
func (h *Handlers) panelSecret() []byte {
	h.panelSecretMu.Lock()
	defer h.panelSecretMu.Unlock()
	if len(h.panelSecretValue) > 0 {
		return h.panelSecretValue
	}
	h.panelSecretValue = h.loadOrCreatePanelSecret()
	return h.panelSecretValue
}

// loadOrCreatePanelSecret читает секрет с диска, а если его нет — создаёт.
// Вызывается под panelSecretMu.
func (h *Handlers) loadOrCreatePanelSecret() []byte {
	if h.dataDir == "" {
		return nil
	}
	path := filepath.Join(h.dataDir, "credentials", "panel_secret.json")

	var cfg struct {
		Secret string `json:"secret"`
	}
	if body, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(body, &cfg) == nil {
			if raw, err := hex.DecodeString(strings.TrimSpace(cfg.Secret)); err == nil && len(raw) >= 16 {
				return raw
			}
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		h.logger.Printf("ERROR generate panel secret: %v", err)
		return nil
	}
	cfg.Secret = hex.EncodeToString(raw)
	if err := fileutil.WriteJSON(path, cfg, 0o600); err != nil {
		// Не смогли сохранить — секрет живёт до перезапуска (панель работает,
		// но после рестарта потребуется повторный вход).
		h.logger.Printf("WARN save panel secret to %s: %v", path, err)
	}
	return raw
}
