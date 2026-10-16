package config

import (
	"path/filepath"
	"testing"

	"github.com/tomasz-tomczyk/crit/internal/testutil"
)

func TestAgentInstantReplies_OffByDefault(t *testing.T) {
	testutil.SetHome(t, t.TempDir())

	cfg := LoadConfig(t.TempDir())

	if cfg.AgentInstantReplies {
		t.Error("agent_instant_replies is on without any config, want off")
	}
}

func TestAgentInstantReplies_GlobalTurnsItOn(t *testing.T) {
	homeDir := t.TempDir()
	testutil.SetHome(t, homeDir)
	writeAuditConfig(t, filepath.Join(homeDir, ".crit.config.json"), `{"agent_instant_replies": true}`)

	cfg := LoadConfig(t.TempDir())

	if !cfg.AgentInstantReplies {
		t.Error("agent_instant_replies = false with a global true, want on")
	}
}

// Global-only like agent_cmd: a repository can neither turn delivery to the
// waiting agent on for a user nor off for one who chose it.
func TestAgentInstantReplies_ProjectCannotChangeIt(t *testing.T) {
	homeDir := t.TempDir()
	testutil.SetHome(t, homeDir)
	projectOn := t.TempDir()
	writeAuditConfig(t, filepath.Join(projectOn, ".crit.config.json"), `{"agent_instant_replies": true}`)

	if LoadConfig(projectOn).AgentInstantReplies {
		t.Error("project config turned agent_instant_replies on")
	}

	writeAuditConfig(t, filepath.Join(homeDir, ".crit.config.json"), `{"agent_instant_replies": true}`)
	projectOff := t.TempDir()
	writeAuditConfig(t, filepath.Join(projectOff, ".crit.config.json"), `{"agent_instant_replies": false}`)

	if !LoadConfig(projectOff).AgentInstantReplies {
		t.Error("project config turned the global agent_instant_replies off")
	}
}
