package memory

import (
	"sort"
	"testing"
)

func TestKindRegistry_Set(t *testing.T) {
	tests := []struct {
		name   string
		kind   Kind
		policy KindPolicy
	}{
		{
			name:   "override a default kind",
			kind:   KindUser, // default is AlwaysInclude=true, MaxEntries=8
			policy: KindPolicy{AlwaysInclude: false, MaxEntries: 2, AgeWeight: 1},
		},
		{
			name:   "register a host-specific kind",
			kind:   Kind("incident"),
			policy: KindPolicy{AlwaysInclude: true, MaxEntries: 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewKindRegistry()
			r.Set(tt.kind, tt.policy)
			if got := r.PolicyFor(tt.kind); got != tt.policy {
				t.Errorf("PolicyFor(%q) = %+v, want %+v", tt.kind, got, tt.policy)
			}
		})
	}
}

// Set must keep AlwaysIncludeKinds consistent: turning a default
// always-include kind off drops it, turning a custom kind on adds it.
func TestKindRegistry_Set_UpdatesAlwaysInclude(t *testing.T) {
	r := NewKindRegistry()
	r.Set(KindUser, KindPolicy{AlwaysInclude: false})        // was true by default
	r.Set(Kind("incident"), KindPolicy{AlwaysInclude: true}) // new always-include

	got := r.AlwaysIncludeKinds()
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []Kind{"feedback", "incident"} // user dropped, feedback still default-on
	if len(got) != len(want) {
		t.Fatalf("AlwaysIncludeKinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AlwaysIncludeKinds = %v, want %v", got, want)
			break
		}
	}
}

func TestKindRegistry_PolicyFor_FallbackForUnknown(t *testing.T) {
	r := NewKindRegistry()
	if got := r.PolicyFor(Kind("never-registered")); got != FallbackKindPolicy {
		t.Errorf("PolicyFor(unknown) = %+v, want FallbackKindPolicy %+v", got, FallbackKindPolicy)
	}
}
