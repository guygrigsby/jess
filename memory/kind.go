package memory

// Kind is the typed identifier for the canonical memory categories.
// Entry.Kind stays untyped (string) so callers can use raw literals
// without conversion — these constants are the names a typed caller
// would use, plus the keys the KindRegistry indexes policies under.
//
// The four constants mirror Claude Code's auto-memory taxonomy. The
// distinction is semantic, not enforced: a host that wants
// "incident" or "decision" as a Kind can use any string. The
// registry has a sensible default for unknown kinds.
type Kind string

const (
	// KindUser: facts about who the user is — role, expertise,
	// long-running goals. Stable. Always loaded eagerly into the
	// prompt; recall doesn't apply (these are CORE memories).
	KindUser Kind = "user"

	// KindFeedback: explicit guidance the user gave about how to
	// approach work — corrections, preferences, "stop doing X."
	// Stable; load eagerly. Treated as policy the model should
	// follow without re-asking.
	KindFeedback Kind = "feedback"

	// KindProject: current work context — goals, decisions,
	// deadlines, incidents. Time-bounded and decays. Recall-only
	// (only loaded when the conversation references the relevant
	// project or topic).
	KindProject Kind = "project"

	// KindReference: pointers to external information — "bugs
	// live in Linear project X", "dashboard at grafana.io/foo".
	// Recall-only; rarely surfaced without an explicit trigger.
	KindReference Kind = "reference"
)

// KindPolicy is the per-Kind retrieval/injection policy the
// ContextManager honors when building the prompt view. Hosts
// override defaults via KindRegistry.Set.
type KindPolicy struct {
	// AlwaysInclude bypasses recall scoring entirely — every
	// entry of this Kind for the agent (up to MaxEntries) is
	// injected on every turn. The right default for "user" and
	// "feedback" Kinds; the wrong default for everything else.
	AlwaysInclude bool

	// MaxEntries caps how many entries of this Kind reach the
	// prompt per turn. 0 means "use the ContextManager's global
	// cap." Bound this conservatively for AlwaysInclude Kinds —
	// each entry is a fixed budget cost.
	MaxEntries int

	// AgeWeight (only used when AlwaysInclude=false) scales the
	// recall-score penalty for older entries: 0 means "ignore
	// age," positive values prefer newer. Useful for KindProject
	// (recent decisions matter more than month-old ones).
	AgeWeight float64
}

// DefaultKindPolicies is the baked-in policy map matching the
// canonical Kinds. Hosts customize via KindRegistry.Set, or
// register entirely new Kinds with their own policy.
//
// Tuning rationale:
//
//   - user/feedback always-include, cap 8 each. Core memories the
//     model should always operate against; 16 entries combined is
//     a few KB of prompt budget — affordable.
//   - project recall-only with age decay; cap 6 to avoid swamping.
//   - reference recall-only without age decay (pointers don't
//     "expire" — a link to a dashboard is still useful months
//     later); cap 4.
var DefaultKindPolicies = map[Kind]KindPolicy{
	KindUser:      {AlwaysInclude: true, MaxEntries: 8},
	KindFeedback:  {AlwaysInclude: true, MaxEntries: 8},
	KindProject:   {AlwaysInclude: false, MaxEntries: 6, AgeWeight: 0.5},
	KindReference: {AlwaysInclude: false, MaxEntries: 4, AgeWeight: 0},
}

// FallbackKindPolicy is what KindRegistry returns for a Kind that
// has no specific policy registered (including unknown / freeform
// Kinds). Behaves like KindReference — recall-only, no age
// decay, modest cap. Safe default that won't bloat prompts.
var FallbackKindPolicy = KindPolicy{AlwaysInclude: false, MaxEntries: 4, AgeWeight: 0}

// KindRegistry holds per-Kind policies. Safe for concurrent reads
// after construction; writes happen at startup. Hosts construct one
// per Agent (so different agents can have different policies) or
// share one across an installation.
type KindRegistry struct {
	policies map[Kind]KindPolicy
}

// NewKindRegistry returns a registry seeded with DefaultKindPolicies.
// Hosts can override + extend via Set.
func NewKindRegistry() *KindRegistry {
	r := &KindRegistry{policies: make(map[Kind]KindPolicy, len(DefaultKindPolicies))}
	for k, p := range DefaultKindPolicies {
		r.policies[k] = p
	}
	return r
}

// Set installs (or replaces) the policy for kind. Pass a custom
// Kind string to register policies for host-specific kinds.
func (r *KindRegistry) Set(kind Kind, policy KindPolicy) {
	r.policies[kind] = policy
}

// PolicyFor returns the policy for kind, falling back to
// FallbackKindPolicy when none is registered. Callers don't need
// to special-case unknown Kinds.
func (r *KindRegistry) PolicyFor(kind Kind) KindPolicy {
	if p, ok := r.policies[kind]; ok {
		return p
	}
	return FallbackKindPolicy
}

// AlwaysIncludeKinds returns the registered Kinds with
// AlwaysInclude=true. The ContextManager uses this to pull
// always-included entries directly from the Store, bypassing
// recall.
func (r *KindRegistry) AlwaysIncludeKinds() []Kind {
	var out []Kind
	for k, p := range r.policies {
		if p.AlwaysInclude {
			out = append(out, k)
		}
	}
	return out
}
