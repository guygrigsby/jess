package acl

import (
	"github.com/guygrigsby/jess/message"
	ac "github.com/voocel/agentcore"
)

// roleToAC maps a jess Role to an agentcore Role. The string values coincide,
// but the mapping is explicit so a divergence is a compile/test failure, not a
// silent mismatch.
func roleToAC(r message.Role) ac.Role {
	switch r {
	case message.RoleUser:
		return ac.RoleUser
	case message.RoleAssistant:
		return ac.RoleAssistant
	case message.RoleSystem:
		return ac.RoleSystem
	case message.RoleTool:
		return ac.RoleTool
	default:
		return ac.Role(r)
	}
}

// roleFromAC maps an agentcore Role to a jess Role.
func roleFromAC(r ac.Role) message.Role {
	switch r {
	case ac.RoleUser:
		return message.RoleUser
	case ac.RoleAssistant:
		return message.RoleAssistant
	case ac.RoleSystem:
		return message.RoleSystem
	case ac.RoleTool:
		return message.RoleTool
	default:
		return message.Role(r)
	}
}
