// Package httpapi exposes the Comms application through versioned HTTP.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/pkg/buildinfo"
)

const (
	ResponseSchema = "comms.response.v1"
	AgentHeader    = "X-Comms-Agent-ID"
)

type responseEnvelope struct {
	Schema string `json:"schema"`
	Data   any    `json:"data"`
}

type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type Handler struct{ app *app.Service }

func NewHandler(service *app.Service) http.Handler {
	h := &Handler{app: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", h.handshake)
	mux.HandleFunc("GET /v1/capabilities", h.capabilities)
	mux.HandleFunc("GET /v1/instructions", h.instructions)
	mux.HandleFunc("GET /v1/openapi.json", h.openapi)
	mux.HandleFunc("GET /v1/health", h.health)
	mux.HandleFunc("GET /v1/doctor", h.doctor)
	mux.HandleFunc("POST /v1/agents/join", h.join)
	mux.HandleFunc("GET /v1/whoami", h.whoami)
	mux.HandleFunc("GET /v1/agents/{agent}", h.getAgent)
	mux.HandleFunc("PATCH /v1/agents/{agent}", h.updateAgent)
	mux.HandleFunc("POST /v1/agents/{agent}/retire", h.retireAgent)
	mux.HandleFunc("GET /v1/agents", h.agents)
	mux.HandleFunc("POST /v1/topics", h.createTopic)
	mux.HandleFunc("PUT /v1/topics/by-external-reference", h.ensureTopic)
	mux.HandleFunc("PATCH /v1/topics/{topic}", h.updateTopic)
	mux.HandleFunc("POST /v1/topics/{topic}/archive", h.archiveTopic)
	mux.HandleFunc("GET /v1/topics", h.topics)
	mux.HandleFunc("PUT /v1/topics/{topic}/subscription", h.follow)
	mux.HandleFunc("DELETE /v1/topics/{topic}/subscription", h.unfollow)
	mux.HandleFunc("GET /v1/subscriptions", h.subscriptions)
	mux.HandleFunc("POST /v1/messages", h.publish)
	mux.HandleFunc("POST /v1/direct-messages", h.directSend)
	mux.HandleFunc("POST /v1/messages/{message}/replies", h.reply)
	mux.HandleFunc("GET /v1/inbox", h.inbox)
	mux.HandleFunc("GET /v1/topics/{topic}/messages", h.topicMessages)
	mux.HandleFunc("GET /v1/messages/{message}/thread", h.thread)
	mux.HandleFunc("GET /v1/messages/{message}", h.peek)
	mux.HandleFunc("POST /v1/messages/{message}/read-through", h.readThrough)
	mux.HandleFunc("GET /v1/messages/{message}/receipts", h.receipts)
	mux.HandleFunc("GET /v1/search", h.search)
	mux.HandleFunc("GET /v1/observe", h.observe)
	mux.HandleFunc("GET /v1/retention", h.retention)
	mux.HandleFunc("POST /v1/purge", h.purge)
	mux.HandleFunc("GET /v1/export", h.export)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateQueryBoundary(r); err != nil {
			h.respond(w, nil, err)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func validateQueryBoundary(r *http.Request) error {
	var operation *help.Operation
	for _, candidate := range help.Operations() {
		if candidate.HTTP != nil && candidate.HTTP.Method == r.Method && matchPath(candidate.HTTP.Path, r.URL.Path) {
			copy := candidate
			operation = &copy
			break
		}
	}
	if operation == nil {
		return nil
	}
	allowed := map[string]help.Parameter{}
	for _, parameter := range operation.Parameters {
		if parameter.Location == help.QueryParameter {
			allowed[parameter.Name] = parameter
		}
	}
	for key, values := range r.URL.Query() {
		parameter, ok := allowed[key]
		if !ok {
			return fmt.Errorf("%w: unknown query parameter %q", domain.ErrInvalid, key)
		}
		for _, value := range values {
			switch parameter.Type {
			case "boolean":
				if _, err := strconv.ParseBool(value); err != nil {
					return fmt.Errorf("%w: %s must be a boolean", domain.ErrInvalid, key)
				}
			case "integer":
				n, err := strconv.Atoi(value)
				if err != nil || n < 1 {
					return fmt.Errorf("%w: %s must be a positive integer", domain.ErrInvalid, key)
				}
			}
		}
	}
	for name, parameter := range allowed {
		if parameter.Required && r.URL.Query().Get(name) == "" {
			return fmt.Errorf("%w: query parameter %s is required", domain.ErrInvalid, name)
		}
	}
	return nil
}

func matchPath(pattern, actual string) bool {
	want, got := strings.Split(strings.Trim(pattern, "/"), "/"), strings.Split(strings.Trim(actual, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if strings.HasPrefix(want[i], "{") && strings.HasSuffix(want[i], "}") {
			continue
		}
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

func (h *Handler) handshake(w http.ResponseWriter, r *http.Request) {
	result, err := h.app.Handshake(r.Context())
	if err == nil {
		result.ServerVersion = buildinfo.Version
		result.Capabilities = result.Capabilities[:0]
		for _, operation := range help.Operations() {
			result.Capabilities = append(result.Capabilities, operation.ID)
		}
	}
	h.respond(w, result, err)
}

func (h *Handler) capabilities(w http.ResponseWriter, _ *http.Request) {
	h.respond(w, help.Capabilities(), nil)
}
func (h *Handler) instructions(w http.ResponseWriter, _ *http.Request) {
	h.respond(w, help.AgentInstructions(), nil)
}
func (h *Handler) openapi(w http.ResponseWriter, _ *http.Request) {
	h.respond(w, help.OpenAPIDocument(), nil)
}
func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	h.respond(w, map[string]string{"status": "ok"}, nil)
}
func (h *Handler) doctor(w http.ResponseWriter, r *http.Request) {
	v, e := h.app.Doctor(r.Context())
	h.respond(w, v, e)
}

func (h *Handler) join(w http.ResponseWriter, r *http.Request) {
	var wire struct {
		app.Mutation
		Handle            string `json:"handle,omitempty"`
		DisplayName       string `json:"display_name,omitempty"`
		Purpose           string `json:"purpose,omitempty"`
		Harness           string `json:"harness,omitempty"`
		Project           string `json:"project,omitempty"`
		SessionRef        string `json:"session_ref,omitempty"`
		ExternalNamespace string `json:"external_namespace,omitempty"`
		ExternalKey       string `json:"external_key,omitempty"`
	}
	if !h.decode(w, r, &wire) {
		return
	}
	req := app.JoinRequest{Mutation: wire.Mutation, Handle: wire.Handle, DisplayName: wire.DisplayName, Purpose: wire.Purpose, Harness: wire.Harness, Project: wire.Project, SessionRef: wire.SessionRef}
	if wire.ExternalNamespace != "" || wire.ExternalKey != "" {
		req.ExternalRef = &app.ExternalRef{Namespace: wire.ExternalNamespace, Key: wire.ExternalKey}
	}
	v, e := h.app.Join(r.Context(), req)
	h.respond(w, v, e)
}

func (h *Handler) whoami(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	v, e := h.app.GetAgent(r.Context(), agent, true)
	h.respond(w, map[string]any{"agent": v, "source": "http_header"}, e)
}
func (h *Handler) getAgent(w http.ResponseWriter, r *http.Request) {
	v, e := h.app.GetAgent(r.Context(), r.PathValue("agent"), false)
	h.respond(w, v, e)
}
func (h *Handler) updateAgent(w http.ResponseWriter, r *http.Request) {
	var req app.UpdateAgentRequest
	if !h.decode(w, r, &req) {
		return
	}
	req.Agent = r.PathValue("agent")
	v, e := h.app.UpdateAgent(r.Context(), req)
	h.respond(w, v, e)
}
func (h *Handler) retireAgent(w http.ResponseWriter, r *http.Request) {
	var mutation app.Mutation
	if !h.decodeOptional(w, r, &mutation) {
		return
	}
	v, e := h.app.RetireAgent(r.Context(), app.RetireAgentRequest{Mutation: mutation, Agent: r.PathValue("agent")})
	h.respond(w, v, e)
}
func (h *Handler) agents(w http.ResponseWriter, r *http.Request) {
	p, e := page(r)
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Agents(r.Context(), app.AgentListRequest{PageRequest: p, IncludeRetired: boolQuery(r, "include_retired")})
	h.respond(w, v, e)
}

func (h *Handler) createTopic(w http.ResponseWriter, r *http.Request) {
	var req app.CreateTopicRequest
	if !h.decode(w, r, &req) {
		return
	}
	v, e := h.app.CreateTopic(r.Context(), req)
	h.respond(w, v, e)
}
func (h *Handler) ensureTopic(w http.ResponseWriter, r *http.Request) {
	var wire struct {
		app.Mutation
		ExternalNamespace string `json:"external_namespace"`
		ExternalKey       string `json:"external_key"`
		Name              string `json:"name"`
		Description       string `json:"description,omitempty"`
	}
	if !h.decode(w, r, &wire) {
		return
	}
	v, e := h.app.EnsureTopic(r.Context(), app.EnsureTopicRequest{Mutation: wire.Mutation, ExternalRef: app.ExternalRef{Namespace: wire.ExternalNamespace, Key: wire.ExternalKey}, Name: wire.Name, Description: wire.Description})
	h.respond(w, v, e)
}
func (h *Handler) updateTopic(w http.ResponseWriter, r *http.Request) {
	var req app.UpdateTopicRequest
	if !h.decode(w, r, &req) {
		return
	}
	req.Topic = r.PathValue("topic")
	v, e := h.app.UpdateTopic(r.Context(), req)
	h.respond(w, v, e)
}
func (h *Handler) archiveTopic(w http.ResponseWriter, r *http.Request) {
	var m app.Mutation
	if !h.decodeOptional(w, r, &m) {
		return
	}
	v, e := h.app.ArchiveTopic(r.Context(), app.ArchiveTopicRequest{Mutation: m, Topic: r.PathValue("topic")})
	h.respond(w, v, e)
}
func (h *Handler) topics(w http.ResponseWriter, r *http.Request) {
	p, e := page(r)
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Topics(r.Context(), r.Header.Get(AgentHeader), app.TopicListRequest{PageRequest: p, IncludeArchived: boolQuery(r, "include_archived"), IncludeDirect: boolQuery(r, "include_direct")})
	h.respond(w, v, e)
}
func (h *Handler) follow(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	var m app.Mutation
	if !h.decodeOptional(w, r, &m) {
		return
	}
	v, e := h.app.Follow(r.Context(), app.FollowRequest{Mutation: m, Agent: agent, Topic: r.PathValue("topic")})
	h.respond(w, v, e)
}
func (h *Handler) unfollow(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	var m app.Mutation
	if !h.decodeOptional(w, r, &m) {
		return
	}
	v, e := h.app.Unfollow(r.Context(), app.UnfollowRequest{Mutation: m, Agent: agent, Topic: r.PathValue("topic")})
	h.respond(w, v, e)
}
func (h *Handler) subscriptions(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	p, e := page(r)
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Subscriptions(r.Context(), app.SubscriptionListRequest{Agent: agent, IncludeUnfollowed: boolQuery(r, "all"), PageRequest: p})
	h.respond(w, v, e)
}

type messageWire struct {
	app.Mutation
	Topic        string          `json:"topic,omitempty"`
	Agent        string          `json:"agent,omitempty"`
	Title        string          `json:"title,omitempty"`
	Body         string          `json:"body"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
	ExpiresIn    string          `json:"expires_in,omitempty"`
	NeverExpires bool            `json:"never_expires,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

func (m messageWire) expiry() (app.Expiry, error) {
	var d time.Duration
	var e error
	if m.ExpiresIn != "" {
		d, e = time.ParseDuration(m.ExpiresIn)
		if e != nil {
			return app.Expiry{}, fmt.Errorf("%w: invalid expires_in: %w", domain.ErrInvalid, e)
		}
	}
	return app.Expiry{Never: m.NeverExpires, At: m.ExpiresAt, After: d}, nil
}
func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	var x messageWire
	if !h.decode(w, r, &x) {
		return
	}
	exp, e := x.expiry()
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Publish(r.Context(), app.PublishRequest{Mutation: x.Mutation, Author: author, Topic: x.Topic, Title: x.Title, Body: x.Body, Expiry: exp, Metadata: x.Metadata})
	h.respond(w, v, e)
}
func (h *Handler) directSend(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	var x messageWire
	if !h.decode(w, r, &x) {
		return
	}
	exp, e := x.expiry()
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.DirectSend(r.Context(), app.DirectSendRequest{Mutation: x.Mutation, Author: author, Recipient: x.Agent, Title: x.Title, Body: x.Body, Expiry: exp, Metadata: x.Metadata})
	h.respond(w, v, e)
}
func (h *Handler) reply(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	var x messageWire
	if !h.decode(w, r, &x) {
		return
	}
	exp, e := x.expiry()
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Reply(r.Context(), app.ReplyRequest{Mutation: x.Mutation, Author: author, Parent: r.PathValue("message"), Title: x.Title, Body: x.Body, Expiry: exp, Metadata: x.Metadata})
	h.respond(w, v, e)
}
func (h *Handler) inbox(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	p, e := page(r)
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Inbox(r.Context(), app.MessageListRequest{PageRequest: p, Agent: agent, UnreadOnly: boolQuery(r, "unread"), ThreadsOnly: boolQuery(r, "threads")})
	h.respond(w, v, e)
}
func (h *Handler) topicMessages(w http.ResponseWriter, r *http.Request) {
	p, e := page(r)
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.TopicMessages(r.Context(), app.MessageListRequest{PageRequest: p, Topic: r.PathValue("topic")})
	h.respond(w, v, e)
}
func (h *Handler) thread(w http.ResponseWriter, r *http.Request) {
	p, e := page(r)
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Thread(r.Context(), app.ThreadRequest{PageRequest: p, Message: r.PathValue("message")})
	h.respond(w, v, e)
}
func (h *Handler) peek(w http.ResponseWriter, r *http.Request) {
	v, e := h.app.Peek(r.Context(), r.PathValue("message"))
	h.respond(w, v, e)
}
func (h *Handler) readThrough(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	var m app.Mutation
	if !h.decodeOptional(w, r, &m) {
		return
	}
	v, e := h.app.ReadThrough(r.Context(), app.ReadThroughRequest{Mutation: m, Agent: agent, Message: r.PathValue("message")})
	h.respond(w, v, e)
}
func (h *Handler) receipts(w http.ResponseWriter, r *http.Request) {
	v, e := h.app.Receipts(r.Context(), r.PathValue("message"))
	h.respond(w, v, e)
}
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	p, e := page(r)
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Search(r.Context(), app.SearchRequest{PageRequest: p, Query: r.URL.Query().Get("query"), From: r.URL.Query().Get("from"), Topic: r.URL.Query().Get("topic")})
	h.respond(w, v, e)
}
func (h *Handler) observe(w http.ResponseWriter, r *http.Request) {
	p, e := page(r)
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	v, e := h.app.Observe(r.Context(), app.ObserveRequest{PageRequest: p, Topic: r.URL.Query().Get("topic")})
	h.respond(w, v, e)
}
func (h *Handler) retention(w http.ResponseWriter, r *http.Request) {
	v, e := h.app.RetentionStatus(r.Context())
	h.respond(w, v, e)
}
func (h *Handler) purge(w http.ResponseWriter, r *http.Request) {
	var req app.PurgeRequest
	if !h.decodeOptional(w, r, &req) {
		return
	}
	v, e := h.app.Purge(r.Context(), req)
	h.respond(w, v, e)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	v, e := h.app.Snapshot(r.Context())
	if e != nil {
		h.respond(w, nil, e)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	write := func(kind string, value any) bool {
		if e := enc.Encode(map[string]any{"schema": "comms.export.v1", "type": kind, "value": value}); e != nil {
			return false
		}
		return true
	}
	if !write("store", map[string]any{"store_id": v.StoreID}) {
		return
	}
	for _, x := range v.Agents {
		if !write("agent", x) {
			return
		}
	}
	for _, x := range v.Aliases {
		if !write("agent_alias", x) {
			return
		}
	}
	for _, x := range v.AgentExternalRefs {
		if !write("agent_external_ref", x) {
			return
		}
	}
	for _, x := range v.Topics {
		if !write("topic", x) {
			return
		}
	}
	for _, x := range v.TopicExternalRefs {
		if !write("topic_external_ref", x) {
			return
		}
	}
	for _, x := range v.Subscriptions {
		if !write("subscription", x) {
			return
		}
	}
	for _, x := range v.Messages {
		if !write("message", x) {
			return
		}
	}
}

func (h *Handler) requireAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := strings.TrimSpace(r.Header.Get(AgentHeader))
	if v == "" {
		h.respond(w, nil, fmt.Errorf("%w: %s header is required", domain.ErrInvalid, AgentHeader))
		return "", false
	}
	return v, true
}
func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	return h.decodeBody(w, r, target, false)
}
func (h *Handler) decodeOptional(w http.ResponseWriter, r *http.Request, target any) bool {
	return h.decodeBody(w, r, target, true)
}
func (h *Handler) decodeBody(w http.ResponseWriter, r *http.Request, target any, optional bool) bool {
	if optional && (r.Body == nil || r.ContentLength == 0) {
		return true
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+(32<<10)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		h.respond(w, nil, fmt.Errorf("%w: invalid JSON body: %w", domain.ErrInvalid, err))
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		h.respond(w, nil, fmt.Errorf("%w: JSON body must contain one object", domain.ErrInvalid))
		return false
	}
	return true
}
func (h *Handler) respond(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		status, code := classify(err)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorBody{Code: code, Message: err.Error(), Details: map[string]any{}}})
		return
	}
	_ = json.NewEncoder(w).Encode(responseEnvelope{Schema: ResponseSchema, Data: value})
}

func classify(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return http.StatusBadRequest, "invalid_argument"
	case errors.Is(err, app.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, app.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable, "timeout"
	case errors.Is(err, app.ErrUnavailable), errors.Is(err, app.ErrOverloaded), errors.Is(err, app.ErrClosed):
		return http.StatusServiceUnavailable, "unavailable"
	default:
		return http.StatusInternalServerError, "internal"
	}
}
func page(r *http.Request) (app.PageRequest, error) {
	p := app.PageRequest{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil {
			return p, fmt.Errorf("%w: limit must be an integer", domain.ErrInvalid)
		}
		p.Limit = n
	}
	return p, nil
}
func boolQuery(r *http.Request, name string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(name))
	return v
}

type Client struct {
	http    *http.Client
	baseURL string
	agent   string
}

func NewUnixClient(socketPath, agent string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}}
	return &Client{http: &http.Client{Transport: transport}, baseURL: "http://comms", agent: agent}
}
func NewTCPClient(baseURL, agent string) *Client {
	return &Client{http: http.DefaultClient, baseURL: strings.TrimRight(baseURL, "/"), agent: agent}
}
func (c *Client) WithAgent(agent string) *Client { copy := *c; copy.agent = agent; return &copy }

func (c *Client) Do(ctx context.Context, method, path string, query url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(raw)
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, e := http.NewRequestWithContext(ctx, method, u, body)
	if e != nil {
		return e
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.agent != "" {
		req.Header.Set(AgentHeader, c.agent)
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return fmt.Errorf("%w: %w", app.ErrUnavailable, e)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		var envelope ErrorEnvelope
		if e := json.NewDecoder(resp.Body).Decode(&envelope); e != nil {
			return fmt.Errorf("http %s", resp.Status)
		}
		switch envelope.Error.Code {
		case "invalid_argument":
			return fmt.Errorf("%w: %s", domain.ErrInvalid, envelope.Error.Message)
		case "not_found":
			return fmt.Errorf("%w: %s", app.ErrNotFound, envelope.Error.Message)
		case "conflict":
			return fmt.Errorf("%w: %s", app.ErrConflict, envelope.Error.Message)
		case "timeout":
			return fmt.Errorf("%w: %s", context.DeadlineExceeded, envelope.Error.Message)
		case "unavailable":
			return fmt.Errorf("%w: %s", app.ErrUnavailable, envelope.Error.Message)
		default:
			return errors.New(envelope.Error.Message)
		}
	}
	if output == nil {
		return nil
	}
	var envelope struct {
		Schema string          `json:"schema"`
		Data   json.RawMessage `json:"data"`
	}
	if e := json.NewDecoder(resp.Body).Decode(&envelope); e != nil {
		return e
	}
	if envelope.Schema != ResponseSchema {
		return fmt.Errorf("%w: unsupported response schema %q", app.ErrConflict, envelope.Schema)
	}
	return json.Unmarshal(envelope.Data, output)
}
func (c *Client) Export(ctx context.Context, w io.Writer) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/export", nil)
	if e != nil {
		return e
	}
	if c.agent != "" {
		req.Header.Set(AgentHeader, c.agent)
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return fmt.Errorf("%w: %w", app.ErrUnavailable, e)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %s", resp.Status)
	}
	_, e = io.Copy(w, resp.Body)
	return e
}
