package host

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/DnzzL/herdr-automations/internal/config"
)

// launcher is the command that starts a's agent. Claude Code runs as a Claude
// account through claude-as, kaneo-work's wrapper, which keeps each account's
// login apart: the automation's account, else kaneo-work's default where the
// machine has its accounts (~/.config/kaneo-work/accounts.json, which the
// Work board writes to every machine). Without either it is plain claude, on
// the machine's own login.
func launcher(a config.Automation, home string) []string {
	if a.Agent != "claude" {
		return []string{a.Agent}
	}
	account := a.Account
	if account == "" {
		account = defaultAccount(home)
	}
	if account == "" {
		return []string{"claude"}
	}
	return []string{"claude-as", account}
}

// defaultAccount is kaneo-work's default Claude account on this machine, or
// "" where it has none.
func defaultAccount(home string) string {
	raw, err := os.ReadFile(filepath.Join(home, ".config", "kaneo-work", "accounts.json"))
	if err != nil {
		return ""
	}
	var accounts struct {
		Default string `json:"default"`
	}
	if json.Unmarshal(raw, &accounts) != nil {
		return ""
	}
	return accounts.Default
}
