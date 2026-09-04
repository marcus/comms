package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTypedIDs(t *testing.T) {
	a, err := NewAgentID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAgentID(string(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAgentID("agt_short"); err == nil {
		t.Fatal("accepted malformed id")
	}
	if _, err := ParseTopicID(string(a)); err == nil {
		t.Fatal("accepted wrong prefix")
	}
}

func TestValidationBoundaries(t *testing.T) {
	messageID, _ := NewMessageID()
	topicID, _ := NewTopicID()
	agentID, _ := NewAgentID()
	tests := []struct {
		name  string
		run   func() error
		valid bool
	}{
		{"handle", func() error { return ValidateHandle("codex.one") }, true},
		{"bad handle", func() error { return ValidateHandle("two words") }, false},
		{"empty topic", func() error { return validateUTF8("topic", "", MaxTopicName, false) }, false},
		{"body max", func() error { return validateUTF8("body", strings.Repeat("x", MaxBody), MaxBody, false) }, true},
		{"body too large", func() error { return validateUTF8("body", strings.Repeat("x", MaxBody+1), MaxBody, false) }, false},
		{"metadata invalid", func() error {
			return Message{ID: messageID, TopicID: topicID, AuthorID: agentID, ThreadRootID: messageID, Body: "body", Metadata: json.RawMessage("{")}.Validate(true)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if (err == nil) != tt.valid {
				t.Fatalf("error=%v valid=%v", err, tt.valid)
			}
		})
	}
}

func TestDirectExternalRefCanonical(t *testing.T) {
	a := AgentID("agt_aaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := AgentID("agt_bbbbbbbbbbbbbbbbbbbbbbbbbb")
	if DirectExternalRef(a, b) != DirectExternalRef(b, a) {
		t.Fatal("not canonical")
	}
}

func TestFixedClockUTC(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	got := (FixedClock{Time: time.Date(2026, 1, 1, 2, 3, 4, 0, loc)}).Now()
	if got.Location() != time.UTC {
		t.Fatalf("location=%v", got.Location())
	}
}
