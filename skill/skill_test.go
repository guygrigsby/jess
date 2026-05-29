package skill

import (
	"reflect"
	"sort"
	"sync"
	"testing"
)

func TestSet_AddAndGet(t *testing.T) {
	s := NewSet()
	skill := Skill{Name: "echo", Description: "echoes input"}
	if err := s.Add(skill); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, ok := s.Get("echo")
	if !ok {
		t.Fatal("Get returned not-found for added skill")
	}
	if got.Description != "echoes input" {
		t.Errorf("Description = %q, want echoes input", got.Description)
	}
}

func TestSet_Add_RejectsEmptyName(t *testing.T) {
	s := NewSet()
	if err := s.Add(Skill{}); err == nil {
		t.Fatal("empty Name should be rejected")
	}
}

func TestSet_Add_RejectsDuplicate(t *testing.T) {
	s := NewSet()
	_ = s.Add(Skill{Name: "echo"})
	if err := s.Add(Skill{Name: "echo"}); err == nil {
		t.Fatal("duplicate Name should be rejected (callers Remove first)")
	}
}

func TestSet_Remove_Idempotent(t *testing.T) {
	s := NewSet()
	_ = s.Add(Skill{Name: "echo"})
	s.Remove("echo")
	// Second Remove on the same name is a no-op, not an error.
	s.Remove("echo")
	// Remove of an absent name is also fine.
	s.Remove("never-existed")
	if _, ok := s.Get("echo"); ok {
		t.Error("Get after Remove should return not-found")
	}
}

func TestSet_Names_Roundtrips(t *testing.T) {
	s := NewSet()
	_ = s.Add(Skill{Name: "a"})
	_ = s.Add(Skill{Name: "b"})
	_ = s.Add(Skill{Name: "c"})
	got := s.Names()
	sort.Strings(got)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Names = %v, want %v", got, want)
	}
}

func TestSet_Get_ReturnsCopy(t *testing.T) {
	s := NewSet()
	_ = s.Add(Skill{Name: "echo", Description: "v1"})
	got, _ := s.Get("echo")
	got.Description = "mutated"
	again, _ := s.Get("echo")
	if again.Description != "v1" {
		t.Errorf("mutating the returned Skill affected the Set: %q", again.Description)
	}
}

// Race regression: concurrent Add / Remove / Get / Names must be
// safe. Hosts will register skills from extension loaders on one
// goroutine and surface them to agentcore on another.
func TestSet_ConcurrentMutation_Safe(t *testing.T) {
	s := NewSet()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "s" + string(rune('a'+i%26))
			_ = s.Add(Skill{Name: name})
			_, _ = s.Get(name)
			_ = s.Names()
			s.Remove(name)
		}(i)
	}
	wg.Wait()
}
