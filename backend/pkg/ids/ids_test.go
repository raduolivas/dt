package ids_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/raduolivas/dt/backend/pkg/ids"
)

func TestNewHasPrefixAndSortsByTime(t *testing.T) {
	a, b := ids.New(ids.Tenant), ids.New(ids.Tenant)
	if !strings.HasPrefix(a, "ten_") || len(a) != len("ten_")+26 || a >= b || a != strings.ToLower(a) {
		t.Fatal(a, b)
	}
	if !ids.Valid("ten", a) || ids.Valid("ak", a) || ids.Valid("ten", "ten_xyz") {
		t.Fatal("Valid")
	}
}

func TestPrefixConstants(t *testing.T) {
	if ids.Tenant != "ten" || ids.APIKey != "ak" || ids.Audit != "aud" || ids.Event != "evt" ||
		ids.Delivery != "del" || ids.Courier != "cou" {
		t.Fatal("prefix constants")
	}
}

func TestNewIsUniqueAndOrderedConcurrently(t *testing.T) {
	const workers, per = 8, 500
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, 0, per)
			for range per {
				local = append(local, ids.New(ids.Event))
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if seen[id] {
					t.Errorf("duplicate %s", id)
				}
				seen[id] = true
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*per {
		t.Fatal(len(seen))
	}
}
