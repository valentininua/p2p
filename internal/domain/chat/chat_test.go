package chat

import (
	"testing"
	"time"
)

func TestMemberIsOnline(t *testing.T) {
	now := time.Now()

	if !NewMember("alice", now.Add(-time.Minute)).IsOnline(now) {
		t.Fatal("expected member to be online")
	}

	if NewMember("bob", now.Add(-PresenceTimeout-time.Second)).IsOnline(now) {
		t.Fatal("expected member to be offline")
	}
}

func TestCountOnline(t *testing.T) {
	now := time.Now()
	members := map[string]*Member{
		"alice": NewMember("alice", now.Add(-time.Second)),
		"bob":   NewMember("bob", now.Add(-PresenceTimeout-time.Second)),
		"carol": nil,
	}

	if got := CountOnline(members, now); got != 1 {
		t.Fatalf("expected 1 online member, got %d", got)
	}
}
