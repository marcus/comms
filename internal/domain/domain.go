// Package domain defines Comms' transport- and persistence-independent model.
package domain

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultRetention = 7 * 24 * time.Hour
	AliasLifetime    = 7 * 24 * time.Hour
	MaxDisplayName   = 128
	MaxHandle        = 64
	MaxTopicName     = 128
	MaxPurpose       = 1024
	MaxSessionField  = 1024
	MaxDescription   = 2048
	MaxTitle         = 512
	MaxBody          = 1 << 20
	MaxMetadata      = 16 << 10
)

var (
	ErrInvalid = errors.New("invalid domain value")
	handleRE   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)
)

type AgentID string
type TopicID string
type MessageID string
type PurgeRunID string

func NewAgentID() (AgentID, error)       { v, e := newID("agt_"); return AgentID(v), e }
func NewTopicID() (TopicID, error)       { v, e := newID("top_"); return TopicID(v), e }
func NewMessageID() (MessageID, error)   { v, e := newID("msg_"); return MessageID(v), e }
func NewPurgeRunID() (PurgeRunID, error) { v, e := newID("prg_"); return PurgeRunID(v), e }

func ParseAgentID(v string) (AgentID, error) {
	if err := validateID(v, "agt_"); err != nil {
		return "", err
	}
	return AgentID(v), nil
}
func ParseTopicID(v string) (TopicID, error) {
	if err := validateID(v, "top_"); err != nil {
		return "", err
	}
	return TopicID(v), nil
}
func ParseMessageID(v string) (MessageID, error) {
	if err := validateID(v, "msg_"); err != nil {
		return "", err
	}
	return MessageID(v), nil
}

func newID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + strings.ToLower(idEncoding.EncodeToString(b)), nil
}

func validateID(v, prefix string) error {
	if !strings.HasPrefix(v, prefix) || len(v) != len(prefix)+26 {
		return fmt.Errorf("%w: malformed %s id", ErrInvalid, strings.TrimSuffix(prefix, "_"))
	}
	raw := v[len(prefix):]
	if raw != strings.ToLower(raw) {
		return fmt.Errorf("%w: id must be lowercase", ErrInvalid)
	}
	b, err := idEncoding.DecodeString(strings.ToUpper(raw))
	if err != nil || len(b) != 16 {
		return fmt.Errorf("%w: malformed id encoding", ErrInvalid)
	}
	return nil
}

type Clock interface{ Now() time.Time }
type UTCClock struct{}

func (UTCClock) Now() time.Time { return time.Now().UTC() }

type FixedClock struct{ Time time.Time }

func (c FixedClock) Now() time.Time { return c.Time.UTC() }

type TopicKind string

const (
	TopicPublic TopicKind = "public"
	TopicDirect TopicKind = "direct"
)

func (k TopicKind) Validate() error {
	if k != TopicPublic && k != TopicDirect {
		return fmt.Errorf("%w: unknown topic kind %q", ErrInvalid, k)
	}
	return nil
}

type Agent struct {
	ID          AgentID    `json:"id"`
	Handle      string     `json:"handle"`
	DisplayName string     `json:"display_name,omitempty"`
	Purpose     string     `json:"purpose,omitempty"`
	Harness     string     `json:"harness,omitempty"`
	Project     string     `json:"project,omitempty"`
	SessionRef  string     `json:"session_ref,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	RetiredAt   *time.Time `json:"retired_at,omitempty"`
}

func (a Agent) Validate() error {
	if _, err := ParseAgentID(string(a.ID)); err != nil {
		return err
	}
	if err := ValidateHandle(a.Handle); err != nil {
		return err
	}
	for name, value := range map[string]struct {
		value string
		max   int
	}{
		"display_name": {a.DisplayName, MaxDisplayName}, "purpose": {a.Purpose, MaxPurpose},
		"harness": {a.Harness, MaxSessionField}, "project": {a.Project, MaxSessionField}, "session_ref": {a.SessionRef, MaxSessionField},
	} {
		if err := validateUTF8(name, value.value, value.max, true); err != nil {
			return err
		}
	}
	return nil
}

func ValidateHandle(v string) error {
	if len(v) == 0 || len(v) > MaxHandle || !handleRE.MatchString(v) {
		return fmt.Errorf("%w: handle must match [a-zA-Z0-9][a-zA-Z0-9._-]* and be at most %d bytes", ErrInvalid, MaxHandle)
	}
	return nil
}

type ExternalRef struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

func (r ExternalRef) Validate() error {
	if err := validateUTF8("external namespace", r.Namespace, 256, false); err != nil {
		return err
	}
	return validateUTF8("external key", r.Key, 2048, false)
}

type Topic struct {
	ID           TopicID    `json:"id"`
	Name         string     `json:"name"`
	Kind         TopicKind  `json:"kind"`
	Description  string     `json:"description,omitempty"`
	NextSequence int64      `json:"next_sequence"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ArchivedAt   *time.Time `json:"archived_at,omitempty"`
}

func (t Topic) Validate() error {
	if _, err := ParseTopicID(string(t.ID)); err != nil {
		return err
	}
	if err := validateUTF8("topic name", t.Name, MaxTopicName, false); err != nil {
		return err
	}
	if err := t.Kind.Validate(); err != nil {
		return err
	}
	return validateUTF8("description", t.Description, MaxDescription, true)
}

type Subscription struct {
	AgentID             AgentID    `json:"agent_id"`
	TopicID             TopicID    `json:"topic_id"`
	FollowedAt          time.Time  `json:"followed_at"`
	UnfollowedAt        *time.Time `json:"unfollowed_at,omitempty"`
	ReadThroughSequence int64      `json:"read_through_sequence"`
	ReadThroughAt       *time.Time `json:"read_through_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type AuthorContext struct {
	Harness    string `json:"harness,omitempty"`
	Project    string `json:"project,omitempty"`
	SessionRef string `json:"session_ref,omitempty"`
}

func (c AuthorContext) Validate() error {
	for name, value := range map[string]string{"harness": c.Harness, "project": c.Project, "session_ref": c.SessionRef} {
		if err := validateUTF8(name, value, MaxSessionField, true); err != nil {
			return err
		}
	}
	return nil
}

type Message struct {
	ID            MessageID       `json:"id"`
	TopicID       TopicID         `json:"topic_id"`
	Sequence      int64           `json:"sequence"`
	AuthorID      AgentID         `json:"author_id"`
	AuthorContext AuthorContext   `json:"author_context"`
	Title         string          `json:"title,omitempty"`
	Body          string          `json:"body"`
	InReplyTo     *MessageID      `json:"in_reply_to,omitempty"`
	ThreadRootID  MessageID       `json:"thread_root_id"`
	CreatedAt     time.Time       `json:"created_at"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

func (m Message) Validate(reply bool) error {
	if _, err := ParseMessageID(string(m.ID)); err != nil {
		return err
	}
	if _, err := ParseTopicID(string(m.TopicID)); err != nil {
		return err
	}
	if _, err := ParseAgentID(string(m.AuthorID)); err != nil {
		return err
	}
	if !reply {
		if err := validateUTF8("title", m.Title, MaxTitle, false); err != nil {
			return err
		}
	} else if err := validateUTF8("title", m.Title, MaxTitle, true); err != nil {
		return err
	}
	if err := validateUTF8("body", m.Body, MaxBody, false); err != nil {
		return err
	}
	if err := m.AuthorContext.Validate(); err != nil {
		return err
	}
	if len(m.Metadata) > MaxMetadata {
		return fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalid, MaxMetadata)
	}
	if len(m.Metadata) > 0 && !json.Valid(m.Metadata) {
		return fmt.Errorf("%w: metadata is not valid JSON", ErrInvalid)
	}
	return nil
}

func validateUTF8(name, value string, max int, optional bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalid, name)
	}
	if !optional && value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	if len(value) > max {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalid, name, max)
	}
	return nil
}

func DirectExternalRef(a, b AgentID) ExternalRef {
	parts := []string{string(a), string(b)}
	if parts[1] < parts[0] {
		parts[0], parts[1] = parts[1], parts[0]
	}
	return ExternalRef{Namespace: "direct", Key: parts[0] + "." + parts[1]}
}

func DefaultExpiry(created time.Time) *time.Time { v := created.UTC().Add(DefaultRetention); return &v }
