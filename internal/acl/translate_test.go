package acl

import (
	"testing"

	"github.com/guygrigsby/jess/message"
	ac "github.com/voocel/agentcore"
)

func TestRoleRoundTrip(t *testing.T) {
	tests := []struct {
		jess message.Role
		acr  ac.Role
	}{
		{message.RoleUser, ac.RoleUser},
		{message.RoleAssistant, ac.RoleAssistant},
		{message.RoleSystem, ac.RoleSystem},
		{message.RoleTool, ac.RoleTool},
	}
	for _, tt := range tests {
		t.Run(string(tt.jess), func(t *testing.T) {
			if got := roleToAC(tt.jess); got != tt.acr {
				t.Errorf("roleToAC(%q) = %q, want %q", tt.jess, got, tt.acr)
			}
			if got := roleFromAC(tt.acr); got != tt.jess {
				t.Errorf("roleFromAC(%q) = %q, want %q", tt.acr, got, tt.jess)
			}
		})
	}
}
