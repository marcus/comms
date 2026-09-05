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
	// IncludeSelf restores the selected agent's own messages in inbox
	// projections. Inbox is an attention surface, so it excludes them by
	// default. It is a query projection only: nothing is deleted, no cursor
	// moves, and topic history, thread, peek, search, observe, receipts, and
	// export are unaffected.
	IncludeSelf bool `json:"include_self,omitempty"`
	Latest      bool `json:"latest,omitempty"`
}

// Wait timeouts are always bounded. A caller may shorten the default but may
// not ask a service to hold a request open indefinitely.
const (
	DefaultWaitTimeout = 30 * time.Second
	MaxWaitTimeout     = time.Hour
)

// AgentWaitRequest awaits logical registration of one addressable handle.
// Registration is the only predicate: it says nothing about whether the
// session's process is alive, idle, or reading its inbox.
type AgentWaitRequest struct {
	Agent   string        `json:"agent"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// MessageWaitRequest awaits unread messages routed to Agent that match the
// optional author and thread filters. Waiting never acknowledges anything.
type MessageWaitRequest struct {
	Agent       string        `json:"agent"`
	From        string        `json:"from,omitempty"`
	Thread      string        `json:"thread,omitempty"`
	After       string        `json:"after,omitempty"`
	Limit       int           `json:"limit,omitempty"`
	IncludeSelf bool          `json:"include_self,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
}

// ResolvedWait is the immutable identity a wait was resolved to once, before
// it started waiting. Handles and thread members are mutable presentation; a
// rename during the wait does not retarget it.
type ResolvedWait struct {
	AgentID      domain.AgentID   `json:"agent_id"`
	FromID       domain.AgentID   `json:"from_id,omitempty"`
	ThreadRootID domain.MessageID `json:"thread_root_id,omitempty"`
}

// MessageWaitResponse carries the matching batch and the continuation cursor a
// caller passes back as After to resume without re-reading the same messages.
// It is distinct from a subscription read cursor and acknowledges nothing.
type MessageWaitResponse struct {
	Items  []domain.Message `json:"items"`
	After  string           `json:"after"`
	Filter ResolvedWait     `json:"filter"`
}
type ThreadRequest struct {
	PageRequest
	Message string `json:"message"`
	Latest  bool   `json:"latest,omitempty"`
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
	ResolveWait(context.Context, MessageWaitRequest, time.Time) (ResolvedWait, error)
	MatchingMessages(context.Context, MessageWaitRequest, ResolvedWait, time.Time) (MessageWaitResponse, error)
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
	agents        AgentStore
	topics        TopicStore
	messageStore  MessageStore
	maintenance   MaintenanceStore
	clock         domain.Clock
	agentEvents   Notifier
	messageEvents Notifier
}

// AgentEvents and MessageEvents expose the service's own change signals. Every
// mutation reaches the store through Service, so Service is the one place that
// can announce a committed change without giving an adapter a second job.
func (s *Service) AgentEvents() EventSource   { return &s.agentEvents }
func (s *Service) MessageEvents() EventSource { return &s.messageEvents }

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
	joined, err := s.agents.JoinAgent(ctx, req, a, now)
	if err == nil {
		s.agentEvents.Notify()
	}
	return joined, err
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
	updated, err := s.agents.UpdateAgent(ctx, req, s.clock.Now())
	if err == nil && req.Handle != nil {
		s.agentEvents.Notify()
	}
	return updated, err
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
	subscription, err := s.topics.Follow(ctx, req, s.clock.Now())
	if err == nil {
		// Following a topic that already holds unread messages routes them to
		// this agent, which can satisfy a wait that is already blocked.
		s.messageEvents.Notify()
	}
	return subscription, err
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
	message, e := fn(ctx, PreparedMessage{Mutation: m, ID: id, Author: author, Topic: topic, Parent: parent, Recipient: recipient, Title: title, Body: body, ExpiresAt: exp, Metadata: metadata, Now: now})
	if e == nil {
		s.messageEvents.Notify()
	}
	return message, e
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

// WaitForAgent blocks until ref resolves to an active agent and returns it.
// It returns immediately when ref already resolves. The wait predicate is
// logical registration in Comms and nothing more: it does not assert that the
// session's provider process is alive, idle, has read a message, or will
// answer one. It never creates, renames, retires, or takes over an identity.
//
// A ref that resolves to a retired agent is a conflict rather than an
// unbounded wait, because a retired handle stays taken. A missing ref waits
// until the bounded deadline expires (timeout) or the caller cancels
// (cancelled); the two are distinguishable in structured output.
func (s *Service) WaitForAgent(ctx context.Context, req AgentWaitRequest) (domain.Agent, error) {
	if strings.TrimSpace(req.Agent) == "" {
		return domain.Agent{}, requiredErr("agent")
	}
	timeout, err := normalizeWaitTimeout(req.Timeout)
	if err != nil {
		return domain.Agent{}, err
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Subscribe before the first lookup so a join committed between the
	// lookup and the wait cannot be missed.
	signals, release := s.agentEvents.Subscribe()
	defer release()
	for {
		agent, err := s.agents.GetAgent(ctx, req.Agent, false, s.clock.Now())
		switch {
		case err == nil && agent.RetiredAt == nil:
			return agent, nil
		case err == nil:
			return domain.Agent{}, fmt.Errorf("%w: agent %q is retired", ErrConflict, req.Agent)
		case !errors.Is(err, ErrNotFound):
			return domain.Agent{}, err
		}
		select {
		case <-signals:
		case <-deadline.Done():
			return domain.Agent{}, waitEnded(ctx, fmt.Sprintf("agent %q did not join within %s", req.Agent, timeout))
		}
	}
}

// WaitForMessages blocks until at least one unread message routed to the
// selected agent matches the optional author and thread filters, and returns
// that bounded batch with a continuation cursor.
//
// Matching is filtered in the store before the limit, so a batch is never
// silently short. Waiting is a read: it does not mark anything read, advance a
// durable read-through cursor, send or auto-reply to anything, or promise that
// the recipient will act. Preexisting unread matches return immediately, and
// the returned After cursor resumes the stream without repeating a message.
// Self-authored messages do not satisfy a wait unless IncludeSelf is set,
// matching the inbox default.
func (s *Service) WaitForMessages(ctx context.Context, req MessageWaitRequest) (MessageWaitResponse, error) {
	if strings.TrimSpace(req.Agent) == "" {
		return MessageWaitResponse{}, requiredErr("agent")
	}
	timeout, err := normalizeWaitTimeout(req.Timeout)
	if err != nil {
		return MessageWaitResponse{}, err
	}
	page, err := PageRequest{Limit: req.Limit}.normalized()
	if err != nil {
		return MessageWaitResponse{}, err
	}
	req.Limit = page.Limit
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Subscribe before resolving and querying so an arrival committed during
	// the first pass still wakes this waiter.
	signals, release := s.messageEvents.Subscribe()
	defer release()
	// Mutable handles and thread members are resolved to stable identity once;
	// a later rename does not retarget an in-flight wait.
	resolved, err := s.messageStore.ResolveWait(ctx, req, s.clock.Now())
	if err != nil {
		return MessageWaitResponse{}, err
	}
	for {
		result, err := s.messageStore.MatchingMessages(ctx, req, resolved, s.clock.Now())
		if err != nil {
			return MessageWaitResponse{}, err
		}
		if len(result.Items) != 0 {
			result.Filter = resolved
			return result, nil
		}
		select {
		case <-signals:
		case <-deadline.Done():
			return MessageWaitResponse{}, waitEnded(ctx, fmt.Sprintf("no matching message arrived within %s", timeout))
		}
	}
}

func normalizeWaitTimeout(d time.Duration) (time.Duration, error) {
	if d == 0 {
		return DefaultWaitTimeout, nil
	}
	if d < 0 {
		return 0, fmt.Errorf("%w: timeout must be positive", domain.ErrInvalid)
	}
	if d > MaxWaitTimeout {
		return 0, fmt.Errorf("%w: timeout must not exceed %s", domain.ErrInvalid, MaxWaitTimeout)
	}
	return d, nil
}

// waitEnded separates a caller that went away from a deadline that expired, so
// structured output can say which happened. Both are exit 5.
func waitEnded(ctx context.Context, description string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return waitTimeout(description)
}

// waitTimeout classifies as context.DeadlineExceeded without embedding the
// sentinel's text, so a transport that re-wraps it does not repeat itself.
type waitTimeout string

func (e waitTimeout) Error() string { return string(e) }
func (waitTimeout) Is(target error) bool {
	return target == context.DeadlineExceeded
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
