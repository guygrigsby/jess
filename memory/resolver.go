package memory

// EntryGetter fetches a stored entry by id. Stores that can resolve an id
// implement it; the provenance ledger uses it to verify a memory ref's hash
// (drift / deletion detection). Stores that cannot resolve simply do not
// implement it, and refs to them are recorded but flagged unverifiable.
type EntryGetter interface {
	Get(id string) (Entry, bool)
}
