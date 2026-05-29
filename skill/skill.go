package skill

import (
	"context"
	"errors"
	"sync"
)

// Skill is one capability bundle. The shape is deliberately minimal —
// real complexity goes into the Tools and the SystemPrompt content,
// not metadata.
//
// A Skill is identified by Name (unique within a Set) and described
// by Description (one-line summary the agent sees in its system
// prompt's skill index). SystemPrompt is a multi-paragraph
// instruction the model receives when the skill is active; this is
// where "use this skill when…" guidance lives. Tools is the
// callable surface contributed when the skill is active.
//
// All fields except Name are optional. A skill can be pure
// instructions (no tools), pure tools (no extra system prompt), or
// both.
type Skill struct {
	Name         string
	Description  string
	SystemPrompt string
	// Tools is the slice of agent tools this skill contributes.
	// Typed as `any` here to keep this package vendor-free: jess's
	// anti-corruption layer type-asserts each entry to jess/tool.Tool
	// when it wires the Set into an agent. Entries that don't
	// implement tool.Tool are ignored.
	Tools []any
}

// Set is a collection of skills keyed by Name. Construct with
// NewSet, mutate with Add / Remove, and hand to an agent via
// jess.WithSkills. Safe for concurrent use.
type Set struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

// NewSet returns an empty Set. Hosts that load skills from disk
// (NewFilesystemLoader, etc.) construct a Set internally and
// return it.
func NewSet() *Set {
	return &Set{skills: make(map[string]Skill)}
}

// Add registers s. Returns an error if a Skill with the same Name
// is already in the Set — callers that intend to replace should
// Remove first. Empty Name is rejected; nothing else is validated.
func (s *Set) Add(skill Skill) error {
	if skill.Name == "" {
		return errors.New("skill: Skill.Name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.skills[skill.Name]; exists {
		return errors.New("skill: skill " + skill.Name + " already registered")
	}
	s.skills[skill.Name] = skill
	return nil
}

// Remove drops the skill with the given name. No-op if absent —
// Remove is idempotent, callers that need to distinguish "was
// present" from "wasn't" should check Get first.
func (s *Set) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skills, name)
}

// Get returns the skill with the given name and a boolean
// indicating presence. The returned Skill is a copy; modifying it
// does not affect the Set.
func (s *Set) Get(name string) (Skill, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[name]
	return sk, ok
}

// Names returns the registered skill names. Order is not
// guaranteed; callers that need stable output sort the result.
func (s *Set) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.skills))
	for name := range s.skills {
		out = append(out, name)
	}
	return out
}

// Loader is the discovery interface. Implementations might walk a
// filesystem (NewFilesystemLoader), call out to a registry, or
// generate skills procedurally. Load is one-shot; hosts that want
// incremental loading wrap a Loader and manage their own Set.
type Loader interface {
	Load(ctx context.Context) (*Set, error)
}
