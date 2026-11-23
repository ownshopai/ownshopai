package main

import "testing"

func TestPickPort(t *testing.T) {
	busy := map[int]bool{2007: true, 2008: true}
	free := func(p int) bool { return !busy[p] }
	none := func(int) bool { return false }
	if p, found, ok := pickPort(0, free, none); !ok || found || p != 2009 {
		t.Fatalf("taken 2007/2008: got %d %v %v", p, found, ok)
	}
	if p, _, _ := pickPort(3001, func(int) bool { return true }, none); p != 3001 {
		t.Fatalf("saved port not reused: %d", p)
	}
	if p, found, _ := pickPort(0, free, func(p int) bool { return p == 2008 }); p != 2008 || !found {
		t.Fatalf("running ownshop not found: %d %v", p, found)
	}
	if _, _, ok := pickPort(0, func(int) bool { return false }, none); ok {
		t.Fatal("no free port should fail")
	}
}
