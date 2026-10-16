package ttl

import (
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string, int](time.Minute)
	c.now = func() time.Time { return now }

	if !c.Add("a", 1) || c.Add("a", 2) {
		t.Fatal("Add should succeed once")
	}
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get = %v %v", v, ok)
	}

	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Error("entry should have expired")
	}
	if !c.Add("a", 3) {
		t.Error("Add should succeed after expiry")
	}

	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Error("entry should be deleted")
	}
}

func TestSweep(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[int, struct{}](time.Second)
	c.now = func() time.Time { return now }
	for i := range 100 {
		c.Set(i, struct{}{})
	}
	now = now.Add(time.Hour)
	for i := 100; i < 128; i++ { // the 128th write triggers a sweep
		c.Set(i, struct{}{})
	}
	if n := len(c.items); n != 28 {
		t.Errorf("items after sweep = %d, want 28", n)
	}
}
