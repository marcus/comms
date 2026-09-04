// Package app implements Comms use cases independently of transports and SQLite.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marcus/comms/internal/domain"
)

const (
	ProtocolVersion = 1
	SchemaVersion   = 1
	DefaultLimit    = 50
	MaxLimit        = 500
)

var (
	ErrNotFound    = errors.New("not found")
	ErrConflict    = errors.New("conflict")
	ErrUnavailable = errors.New("unavailable")
	ErrOverloaded  = errors.New("writer queue full")
	ErrClosed      = errors.New("store closed")
)

type Mutation struct {
	ClientID  string `json:"client_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func (m Mutation) Validate() error {
	if (m.ClientID == "") != (m.RequestID == "") {
		return fmt.Errorf("%w: client_id and request_id must be provided together", domain.ErrInvalid)
	}
	if len(m.ClientID) > 256 || len(m.RequestID) > 256 {
		return fmt.Errorf("%w: idempotency identifier too long", domain.ErrInvalid)
	}
	return nil
}

type PageRequest struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

func (p PageRequest) normalized() (PageRequest, error) {
	if p.Limit < 0 || p.Limit > MaxLimit {
		return p, fmt.Errorf("%w: limit must be between 1 and %d", domain.ErrInvalid, MaxLimit)
	}
	if p.Limit == 0 {
		p.Limit = DefaultLimit
	}
	return p, nil
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type ExternalRef = domain.ExternalRef

type Handshake struct {
	StoreID         string   `json:"store_id"`
	ProtocolVersion int      `json:"protocol_version"`
	SchemaVersion   int      `json:"schema_version"`
	ServerVersion   string   `json:"server_version,omitempty"`
	Capabilities    []string `json:"capabilities"`
}

type JoinRequest struct {
	Mutation
	Handle      string       `json:"handle,omitempty"`
	DisplayName string       `json:"display_name,omitempty"`
	Purpose     string       `json:"purpose,omitempty"`
	Harness     string       `json:"harness,omitempty"`
	Project     string       `json:"project,omitempty"`
	SessionRef  string       `json:"session_ref,omitempty"`
	ExternalRef *ExternalRef `json:"external_ref,omitempty"`
}
type JoinResponse struct {
	Agent    domain.Agent `json:"agent"`
	Rejoined bool         `json:"rejoined"`
}

type UpdateAgentRequest struct {
	Mutation
	Agent       string  `json:"agent"`
	Handle      *string `json:"handle,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	Purpose     *string `json:"purpose,omitempty"`
	Harness     *string `json:"harness,omitempty"`
	Project     *string `json:"project,omitempty"`
	SessionRef  *string `json:"session_ref,omitempty"`
}
type RetireAgentRequest struct {
	Mutation
	Agent string `json:"agent"`
}

type AgentListRequest struct {
	PageRequest
	IncludeRetired bool `json:"include_retired,omitempty"`
}

type CreateTopicRequest struct {
	Mutation
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
type EnsureTopicRequest struct {
	Mutation
	ExternalRef ExternalRef `json:"external_ref"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
}
type EnsureTopicResponse struct {
	Topic   domain.Topic `json:"topic"`
	Created bool         `json:"created"`
}
type UpdateTopicRequest struct {
	Mutation
	Topic       string  `json:"topic"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}
type ArchiveTopicRequest struct {
	Mutation
	Topic string `json:"topic"`
}
type TopicListRequest struct {
	PageRequest
	IncludeArchived bool `json:"include_archived,omitempty"`
	IncludeDirect   bool `json:"include_direct,omitempty"`
}

type FollowRequest struct {
	Mutation
	Agent string `json:"agent"`
	Topic string `json:"topic"`
}
type UnfollowRequest struct {
	Mutation
	Agent string `json:"agent"`
	Topic string `json:"topic"`
}
type SubscriptionListRequest struct {
	Agent             string `json:"agent"`
	IncludeUnfollowed bool   `json:"include_unfollowed,omitempty"`
	PageRequest
}

type Expiry struct {
	Never bool          `json:"never,omitempty"`
	At    *time.Time    `json:"at,omitempty"`
	After time.Duration `json:"after,omitempty"`
}

func (e Expiry) resolve(now time.Time) (*time.Time, error) {
	set := 0
	if e.Never {
		set++
	}
	if e.At != nil {
		set++
	}
	if e.After != 0 {
		set++
	}
	if set > 1 {
		return nil, fmt.Errorf("%w: choose only one expiry override", domain.ErrInvalid)
	}
	if e.Never {
		return nil, nil
	}
	if e.At != nil {
		if !e.At.After(now) {
			return nil, fmt.Errorf("%w: expires_at must be in the future", domain.ErrInvalid)
		}
		v := e.At.UTC()
		return &v, nil
	}
	if e.After < 0 {
		return nil, fmt.Errorf("%w: expiry duration must be positive", domain.ErrInvalid)
	}
	if e.After > 0 {
		v := now.Add(e.After)
		return &v, nil
	}
	return domain.DefaultExpiry(now), nil
}

type PublishRequest struct {
	Mutation
	Author   string          `json:"author"`
	Topic    string          `json:"topic"`
	Title    string          `json:"title"`
	Body     string          `json:"body"`
	Expiry   Expiry          `json:"expiry,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}
type DirectSendRequest struct {
	Mutation
	Author    string          `json:"author"`
	Recipient string          `json:"recipient"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Expiry    Expiry          `json:"expiry,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}
type ReplyRequest struct {
	Mutation
	Author   string          `json:"author"`
	Parent   string          `json:"parent"`
	Title    string          `json:"title,omitempty"`
	Body     string          `json:"body"`
	Expiry   Expiry          `json:"expiry,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// PreparedMessage is validated and fully identified by Service before an atomic store mutation.
type PreparedMessage struct {
	Mutation
	ID        domain.MessageID
	Author    string
	Topic     string
	Parent    string
	Recipient string
	Title     string
	Body      string
	ExpiresAt *time.Time
	Metadata  json.RawMessage
	Now       time.Time
}

type MessageListRequest struct {
	PageRequest
	Agent       string `json:"agent,omitempty"`
	Topic       string `json:"topic,omitempty"`
	UnreadOnly  bool   `json:"unread_only,omitempty"`
	ThreadsOnly bool   `json:"threads_only,omitempty"`
}
type ThreadRequest struct {
	PageRequest
	Message string `json:"message"`
}
type ReadThroughRequest struct {
	Mutation
	Agent   string `json:"agent"`
	Message string `json:"message"`
}
type ReadThroughResponse struct {
	Subscription      domain.Subscription `json:"subscription"`
	PreviousSequence  int64               `json:"previous_sequence"`
	NewSequence       int64               `json:"new_sequence"`
	NewlyAcknowledged int64               `json:"newly_acknowledged"`
}
type Receipt struct {
	Agent  domain.Agent `json:"agent"`
	State  string       `json:"state"`
	ReadAt *time.Time   `json:"read_at,omitempty"`
}
type SearchRequest struct {
	PageRequest
	Query string `json:"query"`
	From  string `json:"from,omitempty"`
	Topic string `json:"topic,omitempty"`
}
type ObserveRequest struct {
	PageRequest
	Topic string `json:"topic,omitempty"`
}

type RetentionStatus struct {
	LiveMessages      int64     `json:"live_messages"`
	ExpiredMessages   int64     `json:"expired_messages"`
	PurgeableMessages int64     `json:"purgeable_messages"`
	LastRun           *PurgeRun `json:"last_run,omitempty"`
}
type PurgeRequest struct {
	Mutation
	DryRun bool `json:"dry_run,omitempty"`
}
type PurgeRun struct {
	ID              domain.PurgeRunID `json:"id"`
	StartedAt       time.Time         `json:"started_at"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
	RemovedMessages int64             `json:"removed_messages"`
	Error           string            `json:"error,omitempty"`
}

type AliasRecord struct {
	Handle    string         `json:"handle"`
	AgentID   domain.AgentID `json:"agent_id"`
	ExpiresAt time.Time      `json:"expires_at"`
}
type ExternalAgentRefRecord struct {
	ExternalRef ExternalRef    `json:"external_ref"`
	AgentID     domain.AgentID `json:"agent_id"`
}
type ExternalTopicRefRecord struct {
	ExternalRef ExternalRef    `json:"external_ref"`
	TopicID     domain.TopicID `json:"topic_id"`
}
type Snapshot struct {
	StoreID           string                   `json:"store_id"`
	Agents            []domain.Agent           `json:"agents"`
	Aliases           []AliasRecord            `json:"aliases"`
	AgentExternalRefs []ExternalAgentRefRecord `json:"agent_external_refs"`
	Topics            []domain.Topic           `json:"topics"`
	TopicExternalRefs []ExternalTopicRefRecord `json:"topic_external_refs"`
	Subscriptions     []domain.Subscription    `json:"subscriptions"`
	Messages          []domain.Message         `json:"messages"`
}
type DoctorReport struct {
	Healthy bool              `json:"healthy"`
	Checks  map[string]string `json:"checks"`
}

type AgentStore interface {
	JoinAgent(context.Context, JoinRequest, domain.Agent, time.Time) (JoinResponse, error)
	GetAgent(context.Context, string, bool, time.Time) (domain.Agent, error)
	UpdateAgent(context.Context, UpdateAgentRequest, time.Time) (domain.Agent, error)
	RetireAgent(context.Context, RetireAgentRequest, time.Time) (domain.Agent, error)
	ListAgents(context.Context, AgentListRequest, time.Time) (Page[domain.Agent], error)
}
type TopicStore interface {
	CreateTopic(context.Context, CreateTopicRequest, domain.Topic, time.Time) (domain.Topic, error)
	EnsureTopic(context.Context, EnsureTopicRequest, domain.Topic, time.Time) (EnsureTopicResponse, error)
	UpdateTopic(context.Context, UpdateTopicRequest, time.Time) (domain.Topic, error)
	ArchiveTopic(context.Context, ArchiveTopicRequest, time.Time) (domain.Topic, error)
	ListTopics(context.Context, TopicListRequest, string, time.Time) (Page[domain.Topic], error)
	Follow(context.Context, FollowRequest, time.Time) (domain.Subscription, error)
	Unfollow(context.Context, UnfollowRequest, time.Time) (domain.Subscription, error)
	ListSubscriptions(context.Context, SubscriptionListRequest, time.Time) (Page[domain.Subscription], error)
}
type MessageStore interface {
	Publish(context.Context, PreparedMessage) (domain.Message, error)
	DirectSend(context.Context, PreparedMessage) (domain.Message, error)
	Reply(context.Context, PreparedMessage) (domain.Message, error)
	Inbox(context.Context, MessageListRequest, time.Time) (Page[domain.Message], error)
	TopicMessages(context.Context, MessageListRequest, time.Time) (Page[domain.Message], error)
	Thread(context.Context, ThreadRequest, time.Time) (Page[domain.Message], error)
	Peek(context.Context, string, time.Time) (domain.Message, error)
	ReadThrough(context.Context, ReadThroughRequest, time.Time) (ReadThroughResponse, error)
	Receipts(context.Context, string, time.Time) ([]Receipt, error)
	Search(context.Context, SearchRequest, time.Time) (Page[domain.Message], error)
	Observe(context.Context, ObserveRequest, time.Time) (Page[domain.Message], error)
}
type MaintenanceStore interface {
	Handshake(context.Context) (Handshake, error)
	RetentionStatus(context.Context, time.Time) (RetentionStatus, error)
	Purge(context.Context, PurgeRequest, domain.PurgeRunID, time.Time) (PurgeRun, error)
	Snapshot(context.Context) (Snapshot, error)
	Doctor(context.Context) (DoctorReport, error)
}
type Store interface {
	AgentStore
	TopicStore
	MessageStore
	MaintenanceStore
}

type Service struct {
	agents       AgentStore
	topics       TopicStore
	messageStore MessageStore
	maintenance  MaintenanceStore
	clock        domain.Clock
}

func NewService(store Store, clock domain.Clock) *Service {
	return NewServiceWithStores(store, store, store, store, clock)
}

// NewServiceWithStores permits focused application tests without a fake that
// implements unrelated persistence capabilities.
func NewServiceWithStores(agents AgentStore, topics TopicStore, messages MessageStore, maintenance MaintenanceStore, clock domain.Clock) *Service {
	if clock == nil {
		clock = domain.UTCClock{}
	}
	return &Service{agents: agents, topics: topics, messageStore: messages, maintenance: maintenance, clock: clock}
}
func (s *Service) Handshake(ctx context.Context) (Handshake, error) {
	return s.maintenance.Handshake(ctx)
}

func (s *Service) Join(ctx context.Context, req JoinRequest) (JoinResponse, error) {
	if err := req.Validate(); err != nil {
		return JoinResponse{}, err
	}
	if req.ExternalRef != nil {
		if err := req.ExternalRef.Validate(); err != nil {
			return JoinResponse{}, err
		}
	}
	id, err := domain.NewAgentID()
	if err != nil {
		return JoinResponse{}, err
	}
	if req.Handle == "" {
		req.Handle = "agent-" + string(id)[4:12]
	}
	now := s.clock.Now()
	a := domain.Agent{ID: id, Handle: req.Handle, DisplayName: req.DisplayName, Purpose: req.Purpose, Harness: req.Harness, Project: req.Project, SessionRef: req.SessionRef, CreatedAt: now, UpdatedAt: now, LastSeenAt: now}
	if err := a.Validate(); err != nil {
		return JoinResponse{}, err
	}
	return s.agents.JoinAgent(ctx, req, a, now)
}
func (s *Service) GetAgent(ctx context.Context, ref string, touch bool) (domain.Agent, error) {
	if strings.TrimSpace(ref) == "" {
		return domain.Agent{}, requiredErr("agent")
	}
	return s.agents.GetAgent(ctx, ref, touch, s.clock.Now())
}
func (s *Service) UpdateAgent(ctx context.Context, req UpdateAgentRequest) (domain.Agent, error) {
	if err := req.Validate(); err != nil {
		return domain.Agent{}, err
	}
	if req.Agent == "" {
		return domain.Agent{}, requiredErr("agent")
	}
	if req.Handle != nil {
		if err := domain.ValidateHandle(*req.Handle); err != nil {
			return domain.Agent{}, err
		}
	}
	return s.agents.UpdateAgent(ctx, req, s.clock.Now())
}
func (s *Service) RetireAgent(ctx context.Context, req RetireAgentRequest) (domain.Agent, error) {
	if err := req.Validate(); err != nil {
		return domain.Agent{}, err
	}
	if req.Agent == "" {
		return domain.Agent{}, requiredErr("agent")
	}
	return s.agents.RetireAgent(ctx, req, s.clock.Now())
}
func (s *Service) Agents(ctx context.Context, req AgentListRequest) (Page[domain.Agent], error) {
	p, e := req.normalized()
	if e != nil {
		return Page[domain.Agent]{}, e
	}
	req.PageRequest = p
	return s.agents.ListAgents(ctx, req, s.clock.Now())
}

func (s *Service) CreateTopic(ctx context.Context, req CreateTopicRequest) (domain.Topic, error) {
	if err := req.Validate(); err != nil {
		return domain.Topic{}, err
	}
	id, e := domain.NewTopicID()
	if e != nil {
		return domain.Topic{}, e
	}
	now := s.clock.Now()
	t := domain.Topic{ID: id, Name: req.Name, Kind: domain.TopicPublic, Description: req.Description, NextSequence: 1, CreatedAt: now, UpdatedAt: now}
	if e = t.Validate(); e != nil {
		return domain.Topic{}, e
	}
	return s.topics.CreateTopic(ctx, req, t, now)
}
func (s *Service) EnsureTopic(ctx context.Context, req EnsureTopicRequest) (EnsureTopicResponse, error) {
	if e := req.Validate(); e != nil {
		return EnsureTopicResponse{}, e
	}
	if e := req.ExternalRef.Validate(); e != nil {
		return EnsureTopicResponse{}, e
	}
	if req.ExternalRef.Namespace == "direct" {
		return EnsureTopicResponse{}, fmt.Errorf("%w: external namespace direct is reserved", domain.ErrInvalid)
	}
	id, e := domain.NewTopicID()
	if e != nil {
		return EnsureTopicResponse{}, e
	}
	now := s.clock.Now()
	t := domain.Topic{ID: id, Name: req.Name, Kind: domain.TopicPublic, Description: req.Description, NextSequence: 1, CreatedAt: now, UpdatedAt: now}
	if e = t.Validate(); e != nil {
		return EnsureTopicResponse{}, e
	}
	return s.topics.EnsureTopic(ctx, req, t, now)
}
func (s *Service) UpdateTopic(ctx context.Context, req UpdateTopicRequest) (domain.Topic, error) {
	if e := req.Validate(); e != nil {
		return domain.Topic{}, e
	}
	if req.Topic == "" {
		return domain.Topic{}, requiredErr("topic")
	}
	if req.Name != nil {
		tmp := domain.Topic{ID: domain.TopicID("top_aaaaaaaaaaaaaaaaaaaaaaaaaa"), Name: *req.Name, Kind: domain.TopicPublic}
		if e := tmp.Validate(); e != nil {
			return domain.Topic{}, e
		}
	}
	return s.topics.UpdateTopic(ctx, req, s.clock.Now())
}
func (s *Service) ArchiveTopic(ctx context.Context, req ArchiveTopicRequest) (domain.Topic, error) {
	if e := req.Validate(); e != nil {
		return domain.Topic{}, e
	}
	if req.Topic == "" {
		return domain.Topic{}, requiredErr("topic")
	}
	return s.topics.ArchiveTopic(ctx, req, s.clock.Now())
}
func (s *Service) Topics(ctx context.Context, agent string, req TopicListRequest) (Page[domain.Topic], error) {
	p, e := req.normalized()
	if e != nil {
		return Page[domain.Topic]{}, e
	}
	req.PageRequest = p
	return s.topics.ListTopics(ctx, req, agent, s.clock.Now())
}
func (s *Service) Follow(ctx context.Context, req FollowRequest) (domain.Subscription, error) {
	if e := req.Validate(); e != nil {
		return domain.Subscription{}, e
	}
	if req.Agent == "" || req.Topic == "" {
		return domain.Subscription{}, requiredErr("agent and topic")
	}
	return s.topics.Follow(ctx, req, s.clock.Now())
}
func (s *Service) Unfollow(ctx context.Context, req UnfollowRequest) (domain.Subscription, error) {
	if e := req.Validate(); e != nil {
		return domain.Subscription{}, e
	}
	if req.Agent == "" || req.Topic == "" {
		return domain.Subscription{}, requiredErr("agent and topic")
	}
	return s.topics.Unfollow(ctx, req, s.clock.Now())
}
func (s *Service) Subscriptions(ctx context.Context, req SubscriptionListRequest) (Page[domain.Subscription], error) {
	p, e := req.normalized()
	if e != nil {
		return Page[domain.Subscription]{}, e
	}
	req.PageRequest = p
	return s.topics.ListSubscriptions(ctx, req, s.clock.Now())
}

func (s *Service) Publish(ctx context.Context, req PublishRequest) (domain.Message, error) {
	if e := req.Validate(); e != nil {
		return domain.Message{}, e
	}
	return s.prepareAnd(ctx, req.Mutation, req.Author, req.Topic, "", "", req.Title, req.Body, req.Expiry, req.Metadata, s.messageStore.Publish)
}
func (s *Service) DirectSend(ctx context.Context, req DirectSendRequest) (domain.Message, error) {
	if e := req.Validate(); e != nil {
		return domain.Message{}, e
	}
	return s.prepareAnd(ctx, req.Mutation, req.Author, "", "", req.Recipient, req.Title, req.Body, req.Expiry, req.Metadata, s.messageStore.DirectSend)
}
func (s *Service) Reply(ctx context.Context, req ReplyRequest) (domain.Message, error) {
	if e := req.Validate(); e != nil {
		return domain.Message{}, e
	}
	return s.prepareAnd(ctx, req.Mutation, req.Author, "", req.Parent, "", req.Title, req.Body, req.Expiry, req.Metadata, s.messageStore.Reply)
}
func (s *Service) prepareAnd(ctx context.Context, m Mutation, author, topic, parent, recipient, title, body string, expiry Expiry, metadata json.RawMessage, fn func(context.Context, PreparedMessage) (domain.Message, error)) (domain.Message, error) {
	now := s.clock.Now()
	exp, e := expiry.resolve(now)
	if e != nil {
		return domain.Message{}, e
	}
	id, e := domain.NewMessageID()
	if e != nil {
		return domain.Message{}, e
	}
	if author == "" {
		return domain.Message{}, requiredErr("author")
	}
	if topic == "" && parent == "" && recipient == "" {
		return domain.Message{}, requiredErr("destination")
	}
	probe := domain.Message{ID: id, TopicID: domain.TopicID("top_aaaaaaaaaaaaaaaaaaaaaaaaaa"), AuthorID: domain.AgentID("agt_aaaaaaaaaaaaaaaaaaaaaaaaaa"), ThreadRootID: id, Title: title, Body: body, ExpiresAt: exp, Metadata: metadata}
	if e = probe.Validate(parent != ""); e != nil {
		return domain.Message{}, e
	}
	return fn(ctx, PreparedMessage{Mutation: m, ID: id, Author: author, Topic: topic, Parent: parent, Recipient: recipient, Title: title, Body: body, ExpiresAt: exp, Metadata: metadata, Now: now})
}
func (s *Service) Inbox(ctx context.Context, req MessageListRequest) (Page[domain.Message], error) {
	if req.Agent == "" {
		return Page[domain.Message]{}, requiredErr("agent")
	}
	return s.listMessages(ctx, req, s.messageStore.Inbox)
}
func (s *Service) TopicMessages(ctx context.Context, req MessageListRequest) (Page[domain.Message], error) {
	if req.Topic == "" {
		return Page[domain.Message]{}, requiredErr("topic")
	}
	return s.listMessages(ctx, req, s.messageStore.TopicMessages)
}
func (s *Service) listMessages(ctx context.Context, req MessageListRequest, fn func(context.Context, MessageListRequest, time.Time) (Page[domain.Message], error)) (Page[domain.Message], error) {
	p, e := req.normalized()
	if e != nil {
		return Page[domain.Message]{}, e
	}
	req.PageRequest = p
	return fn(ctx, req, s.clock.Now())
}
func (s *Service) Thread(ctx context.Context, req ThreadRequest) (Page[domain.Message], error) {
	p, e := req.normalized()
	if e != nil {
		return Page[domain.Message]{}, e
	}
	if req.Message == "" {
		return Page[domain.Message]{}, requiredErr("message")
	}
	req.PageRequest = p
	return s.messageStore.Thread(ctx, req, s.clock.Now())
}
func (s *Service) Peek(ctx context.Context, message string) (domain.Message, error) {
	if message == "" {
		return domain.Message{}, requiredErr("message")
	}
	return s.messageStore.Peek(ctx, message, s.clock.Now())
}
func (s *Service) ReadThrough(ctx context.Context, req ReadThroughRequest) (ReadThroughResponse, error) {
	if e := req.Validate(); e != nil {
		return ReadThroughResponse{}, e
	}
	if req.Agent == "" || req.Message == "" {
		return ReadThroughResponse{}, requiredErr("agent and message")
	}
	return s.messageStore.ReadThrough(ctx, req, s.clock.Now())
}
func (s *Service) Receipts(ctx context.Context, message string) ([]Receipt, error) {
	if message == "" {
		return nil, requiredErr("message")
	}
	return s.messageStore.Receipts(ctx, message, s.clock.Now())
}
func (s *Service) Search(ctx context.Context, req SearchRequest) (Page[domain.Message], error) {
	if strings.TrimSpace(req.Query) == "" {
		return Page[domain.Message]{}, requiredErr("query")
	}
	p, e := req.normalized()
	if e != nil {
		return Page[domain.Message]{}, e
	}
	req.PageRequest = p
	return s.messageStore.Search(ctx, req, s.clock.Now())
}
func (s *Service) Observe(ctx context.Context, req ObserveRequest) (Page[domain.Message], error) {
	p, e := req.normalized()
	if e != nil {
		return Page[domain.Message]{}, e
	}
	req.PageRequest = p
	return s.messageStore.Observe(ctx, req, s.clock.Now())
}
func (s *Service) RetentionStatus(ctx context.Context) (RetentionStatus, error) {
	return s.maintenance.RetentionStatus(ctx, s.clock.Now())
}
func (s *Service) Purge(ctx context.Context, req PurgeRequest) (PurgeRun, error) {
	if e := req.Validate(); e != nil {
		return PurgeRun{}, e
	}
	id, e := domain.NewPurgeRunID()
	if e != nil {
		return PurgeRun{}, e
	}
	return s.maintenance.Purge(ctx, req, id, s.clock.Now())
}
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error)   { return s.maintenance.Snapshot(ctx) }
func (s *Service) Doctor(ctx context.Context) (DoctorReport, error) { return s.maintenance.Doctor(ctx) }

func requiredErr(name string) error { return fmt.Errorf("%w: %s is required", domain.ErrInvalid, name) }
