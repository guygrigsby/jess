package core

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/memory"
)

// memResolver adapts a memory.EntryGetter to ledger.Resolver for drift checks.
type memResolver struct{ g memory.EntryGetter }

func (m memResolver) CurrentHash(src ledger.RefSource, id string) (string, bool) {
	if src != ledger.RefMemory || m.g == nil {
		return "", false
	}
	e, ok := m.g.Get(id)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(e.Text))
	return hex.EncodeToString(sum[:]), true
}
