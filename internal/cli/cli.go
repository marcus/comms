// Package cli implements the Comms command-line transport.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/internal/service"
	"github.com/marcus/comms/pkg/buildinfo"
)

const cliSchema = "comms.cli.v1"

type Env struct {
	Args    []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Context context.Context
	Getenv  func(string) string
}
type globals struct {
	json    bool
	help    bool
	as      string
	socket  string
	timeout time.Duration
}
type runner struct {
	env Env
	g   globals
}

func Run(env Env) int {
	if env.Stdin == nil {
		env.Stdin = strings.NewReader("")
	}
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	if env.Context == nil {
		env.Context = context.Background()
	}
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	g, args, err := parseGlobals(env.Args)
	if err != nil {
		return fail(env, false, err)
	}
	r := &runner{env: env, g: g}
	if g.help {
		_, _ = io.WriteString(env.Stdout, help.CLIUsage("comms"))
		return 0
	}
	if len(args) == 0 {
		_, _ = io.WriteString(env.Stdout, help.CLIUsage("comms"))
		return 0
	}
	if err = r.run(args); err != nil {
		return fail(env, g.json, err)
	}
	return 0
}

func (r *runner) run(args []string) error {
	switch args[0] {
	case "help", "-h", "--help":
		_, _ = io.WriteString(r.env.Stdout, help.CLIUsage("comms"))
		return nil
	case "version":
		if len(args) != 1 {
			return usage("version accepts no arguments")
		}
		if r.g.json {
			return r.output(map[string]string{"version": buildinfo.Version, "commit": buildinfo.Commit})
		}
		_, _ = fmt.Fprintf(r.env.Stdout, "comms %s (%s)\n", buildinfo.Version, buildinfo.Commit)
		return nil
	case "capabilities":
		if len(args) != 1 {
			return usage("capabilities accepts no arguments")
		}
		if r.g.json {
			data, e := help.CapabilitiesJSON()
			if e == nil {
				_, e = r.env.Stdout.Write(data)
			}
			return e
		}
		return r.output(help.Capabilities())
	case "openapi":
		if len(args) != 1 {
			return usage("openapi accepts no arguments")
		}
		data, e := help.OpenAPIJSON()
		if e == nil {
			_, e = r.env.Stdout.Write(data)
		}
		return e
	case "instructions":
		if len(args) != 1 {
			return usage("instructions accepts no arguments")
		}
		if r.g.json {
			data, e := help.InstructionsJSON()
			if e == nil {
				_, e = r.env.Stdout.Write(data)
			}
			return e
		}
		_, _ = io.WriteString(r.env.Stdout, help.InstructionsText())
		return nil
	case "serve":
		return r.serve(args[1:])
	case "hello":
		return r.get("/v1/hello", nil, false)
	case "health":
		return r.get("/v1/health", nil, false)
	case "doctor":
		return r.get("/v1/doctor", nil, false)
	case "join":
		return r.join(args[1:])
	case "whoami":
		return r.whoami(args[1:])
	case "agents":
		return r.listAgents(args[1:])
	case "agent":
		return r.agent(args[1:])
	case "topic":
		return r.topic(args[1:])
	case "topics":
		return r.listTopics(args[1:])
	case "subscriptions":
		return r.listSubscriptions(args[1:])
	case "publish":
		return r.publish(args[1:])
	case "send":
		return r.send(args[1:])
	case "reply":
		return r.reply(args[1:])
	case "inbox":
		return r.inbox(args[1:])
	case "peek":
		return r.oneMessageGet(args[1:], "")
	case "read-through":
		return r.oneMessagePost(args[1:], "/read-through")
	case "receipts":
		return r.oneMessageGet(args[1:], "/receipts")
	case "thread":
		return r.thread(args[1:])
	case "search":
		return r.search(args[1:])
	case "observe":
		return r.observe(args[1:])
	case "retention":
		if len(args) == 2 && args[1] == "status" {
			return r.get("/v1/retention", nil, false)
		}
		return usage("usage: comms retention status")
	case "purge":
		return r.purge(args[1:])
	case "export":
		return r.export(args[1:])
	default:
		return usage(fmt.Sprintf("unknown command %q; run 'comms help'", args[0]))
	}
}

func (r *runner) serve(args []string) error {
	fs := newFlagSet("serve")
	listen := fs.String("listen", "", "")
	socket := fs.String("socket", r.g.socket, "")
	database := fs.String("db", "", "")
	daemonChild := fs.Bool("daemon-child", false, "")
	supervised := fs.Bool("supervised", false, "")
	if err := fs.Parse(args); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() != 0 {
		return usage("serve accepts flags only")
	}
	if *daemonChild && *supervised {
		return usage("--daemon-child and --supervised are mutually exclusive")
	}
	mode := service.LaunchModeForeground
	if *daemonChild {
		mode = service.LaunchModeAuto
	}
	if *supervised {
		mode = service.LaunchModeSupervised
	}
	path := *socket
	if path == "" {
		var err error
		path, err = service.ResolveSocketPath(true)
		if err != nil {
			return err
		}
	}
	if *listen == "" {
		_, _ = fmt.Fprintf(r.env.Stdout, "comms: serving on %s\n", path)
	} else {
		_, _ = fmt.Fprintf(r.env.Stdout, "comms: serving on http://%s\n", *listen)
	}
	return service.Run(r.env.Context, service.Config{DatabasePath: *database, SocketPath: path, Listen: *listen, LaunchMode: mode})
}

func (r *runner) join(args []string) error {
	handle := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		handle = args[0]
		args = args[1:]
	}
	fs := newFlagSet("join")
	display := fs.String("display-name", "", "")
	purpose := fs.String("purpose", "", "")
	harness := fs.String("harness", "", "")
	project := fs.String("project", "", "")
	sessionRef := fs.String("session-ref", "", "")
	extNS := fs.String("external-namespace", "", "")
	extKey := fs.String("external-key", "", "")
	contextPath := fs.String("context", r.env.Getenv("COMMS_CONTEXT"), "")
	if err := fs.Parse(args); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() != 0 {
		return usage("unexpected join arguments")
	}
	if (*extNS == "") != (*extKey == "") {
		return usage("--external-namespace and --external-key must be provided together")
	}
	if *contextPath == "" {
		var err error
		*contextPath, err = defaultContextPath(true, r.env.Getenv)
		if err != nil {
			return err
		}
	}
	record, err := readContext(*contextPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if record.ClientID == "" {
		record.ClientID, err = newClientID()
		if err != nil {
			return err
		}
	}
	requestID, err := newRequestID()
	if err != nil {
		return err
	}
	req := map[string]any{"client_id": record.ClientID, "request_id": requestID, "handle": handle, "display_name": *display, "purpose": *purpose, "harness": *harness, "project": *project, "session_ref": *sessionRef}
	if *extNS != "" {
		req["external_namespace"] = *extNS
		req["external_key"] = *extKey
	}
	var response app.JoinResponse
	client, err := r.client(false)
	if err != nil {
		return err
	}
	if err = r.do(client, http.MethodPost, "/v1/agents/join", nil, req, &response); err != nil {
		return err
	}
	record.AgentID = string(response.Agent.ID)
	record.Harness = *harness
	record.Project = *project
	record.SessionRef = *sessionRef
	if err = writeContext(*contextPath, record); err != nil {
		return fmt.Errorf("write context: %w", err)
	}
	return r.output(map[string]any{"agent": response.Agent, "rejoined": response.Rejoined, "context": *contextPath, "identity_source": "context"})
}

func (r *runner) whoami(args []string) error {
	if len(args) != 0 {
		return usage("whoami accepts no arguments")
	}
	identity, client, err := r.identityClient()
	if err != nil {
		return err
	}
	var response struct {
		Agent  domain.Agent `json:"agent"`
		Source string       `json:"source"`
	}
	if err = r.do(client, http.MethodGet, "/v1/whoami", nil, nil, &response); err != nil {
		return err
	}
	response.Source = identity.Source
	return r.output(response)
}
func (r *runner) listAgents(args []string) error {
	q, e := listQuery("agents", args, false)
	if e != nil {
		return e
	}
	return r.get("/v1/agents", q, false)
}
func (r *runner) listTopics(args []string) error {
	q, e := listQuery("topics", args, false)
	if e != nil {
		return e
	}
	return r.get("/v1/topics", q, false)
}
func (r *runner) listSubscriptions(args []string) error {
	q, e := listQuery("subscriptions", args, true)
	if e != nil {
		return e
	}
	return r.get("/v1/subscriptions", q, true)
}

func (r *runner) agent(args []string) error {
	if len(args) < 2 {
		return usage("usage: comms agent get|update|retire AGENT")
	}
	action, ref := args[0], args[1]
	path := "/v1/agents/" + url.PathEscape(ref)
	switch action {
	case "get":
		if len(args) != 2 {
			return usage("agent get accepts one agent")
		}
		return r.get(path, nil, false)
	case "retire":
		if len(args) != 2 {
			return usage("agent retire accepts one agent")
		}
		return r.mutate(http.MethodPost, path+"/retire", map[string]any{}, false)
	case "update":
		fs := newFlagSet("agent update")
		handle := fs.String("handle", "", "")
		display := fs.String("display-name", "", "")
		purpose := fs.String("purpose", "", "")
		harness := fs.String("harness", "", "")
		project := fs.String("project", "", "")
		sessionRef := fs.String("session-ref", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return usage(err.Error())
		}
		if fs.NArg() != 0 {
			return usage("unexpected agent update arguments")
		}
		body := map[string]any{}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "handle":
				body["handle"] = *handle
			case "display-name":
				body["display_name"] = *display
			case "purpose":
				body["purpose"] = *purpose
			case "harness":
				body["harness"] = *harness
			case "project":
				body["project"] = *project
			case "session-ref":
				body["session_ref"] = *sessionRef
			}
		})
		if len(body) == 0 {
			return usage("agent update requires at least one changed field")
		}
		return r.mutate(http.MethodPatch, path, body, false)
	default:
		return usage("usage: comms agent get|update|retire AGENT")
	}
}

func (r *runner) topic(args []string) error {
	if len(args) == 0 {
		return usage("usage: comms topic create|ensure|update|follow|unfollow|archive|messages")
	}
	action := args[0]
	args = args[1:]
	switch action {
	case "create":
		if len(args) == 0 {
			return usage("topic create requires NAME")
		}
		name := args[0]
		fs := newFlagSet("topic create")
		description := fs.String("description", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return usage(err.Error())
		}
		if fs.NArg() != 0 {
			return usage("unexpected topic create arguments")
		}
		return r.mutate(http.MethodPost, "/v1/topics", map[string]any{"name": name, "description": *description}, false)
	case "ensure":
		fs := newFlagSet("topic ensure")
		ns := fs.String("external-namespace", "", "")
		key := fs.String("external-key", "", "")
		name := fs.String("name", "", "")
		description := fs.String("description", "", "")
		if err := fs.Parse(args); err != nil {
			return usage(err.Error())
		}
		if fs.NArg() != 0 || *ns == "" || *key == "" || *name == "" {
			return usage("topic ensure requires --external-namespace, --external-key, and --name")
		}
		return r.mutate(http.MethodPut, "/v1/topics/by-external-reference", map[string]any{"external_namespace": *ns, "external_key": *key, "name": *name, "description": *description}, false)
	case "update":
		if len(args) == 0 {
			return usage("topic update requires TOPIC")
		}
		ref := args[0]
		fs := newFlagSet("topic update")
		name := fs.String("name", "", "")
		description := fs.String("description", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return usage(err.Error())
		}
		if fs.NArg() != 0 {
			return usage("unexpected topic update arguments")
		}
		body := map[string]any{}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "name":
				body["name"] = *name
			case "description":
				body["description"] = *description
			}
		})
		if len(body) == 0 {
			return usage("topic update requires --name or --description")
		}
		return r.mutate(http.MethodPatch, "/v1/topics/"+url.PathEscape(ref), body, false)
	case "follow", "unfollow", "archive":
		if len(args) != 1 {
			return usage("topic " + action + " requires one TOPIC")
		}
		method := http.MethodPut
		suffix := "/subscription"
		identity := true
		if action == "unfollow" {
			method = http.MethodDelete
		}
		if action == "archive" {
			method = http.MethodPost
			suffix = "/archive"
			identity = false
		}
		return r.mutate(method, "/v1/topics/"+url.PathEscape(args[0])+suffix, map[string]any{}, identity)
	case "messages":
		if len(args) == 0 {
			return usage("topic messages requires TOPIC")
		}
		q, e := listQuery("topic messages", args[1:], false)
		if e != nil {
			return e
		}
		return r.get("/v1/topics/"+url.PathEscape(args[0])+"/messages", q, false)
	default:
		return usage("usage: comms topic create|ensure|update|follow|unfollow|archive|messages")
	}
}

func (r *runner) publish(args []string) error {
	if len(args) == 0 {
		return usage("publish requires TOPIC")
	}
	content, e := r.messageContent("publish", args[1:], true)
	if e != nil {
		return e
	}
	content["topic"] = args[0]
	return r.mutate(http.MethodPost, "/v1/messages", content, true)
}
func (r *runner) send(args []string) error {
	if len(args) == 0 {
		return usage("send requires @AGENT")
	}
	recipient := strings.TrimPrefix(args[0], "@")
	if recipient == "" {
		return usage("send requires @AGENT")
	}
	content, e := r.messageContent("send", args[1:], true)
	if e != nil {
		return e
	}
	content["agent"] = recipient
	return r.mutate(http.MethodPost, "/v1/direct-messages", content, true)
}
func (r *runner) reply(args []string) error {
	if len(args) == 0 {
		return usage("reply requires MESSAGE_ID")
	}
	content, e := r.messageContent("reply", args[1:], false)
	if e != nil {
		return e
	}
	return r.mutate(http.MethodPost, "/v1/messages/"+url.PathEscape(args[0])+"/replies", content, true)
}

func (r *runner) messageContent(name string, args []string, titleRequired bool) (map[string]any, error) {
	stdinSelected := false
	if len(args) > 0 && args[len(args)-1] == "-" {
		stdinSelected = true
		args = args[:len(args)-1]
	}
	fs := newFlagSet(name)
	title := fs.String("title", "", "")
	body := fs.String("body", "", "")
	bodyFile := fs.String("body-file", "", "")
	expiresAt := fs.String("expires-at", "", "")
	expiresIn := fs.String("expires-in", "", "")
	never := fs.Bool("never-expires", false, "")
	metadata := fs.String("metadata-json", "", "")
	if err := fs.Parse(args); err != nil {
		return nil, usage(err.Error())
	}
	if fs.NArg() != 0 {
		return nil, usage("unexpected " + name + " arguments")
	}
	if titleRequired && *title == "" {
		return nil, usage(name + " requires --title")
	}
	sources := 0
	if wasSet(fs, "body") {
		sources++
	}
	if *bodyFile != "" {
		sources++
	}
	if stdinSelected {
		sources++
	}
	if sources != 1 {
		return nil, usage("choose exactly one of --body, --body-file, or -")
	}
	text := *body
	var err error
	if *bodyFile != "" {
		data, e := os.ReadFile(*bodyFile)
		err = e
		text = string(data)
	} else if stdinSelected {
		data, e := io.ReadAll(io.LimitReader(r.env.Stdin, domain.MaxBody+1))
		err = e
		text = string(data)
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{"title": *title, "body": text}
	expiryCount := 0
	if *expiresAt != "" {
		expiryCount++
		parsed, e := time.Parse(time.RFC3339Nano, *expiresAt)
		if e != nil {
			return nil, usage("invalid --expires-at")
		}
		result["expires_at"] = parsed
	}
	if *expiresIn != "" {
		expiryCount++
		if _, e := time.ParseDuration(*expiresIn); e != nil {
			return nil, usage("invalid --expires-in")
		}
		result["expires_in"] = *expiresIn
	}
	if *never {
		expiryCount++
		result["never_expires"] = true
	}
	if expiryCount > 1 {
		return nil, usage("choose only one expiration override")
	}
	if *metadata != "" {
		raw := json.RawMessage(*metadata)
		if !json.Valid(raw) {
			return nil, usage("--metadata-json must be valid JSON")
		}
		result["metadata"] = raw
	}
	return result, nil
}

func (r *runner) inbox(args []string) error {
	fs := newFlagSet("inbox")
	unread := fs.Bool("unread", false, "")
	threads := fs.Bool("threads", false, "")
	limit := fs.Int("limit", 0, "")
	cursor := fs.String("cursor", "", "")
	if err := fs.Parse(args); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() != 0 {
		return usage("unexpected inbox arguments")
	}
	q := url.Values{}
	setInt(q, "limit", *limit)
	set(q, "cursor", *cursor)
	setBool(q, "unread", *unread)
	setBool(q, "threads", *threads)
	return r.get("/v1/inbox", q, true)
}
func (r *runner) oneMessageGet(args []string, suffix string) error {
	if len(args) != 1 {
		return usage("command requires one MESSAGE_ID")
	}
	return r.get("/v1/messages/"+url.PathEscape(args[0])+suffix, nil, false)
}
func (r *runner) oneMessagePost(args []string, suffix string) error {
	if len(args) != 1 {
		return usage("command requires one MESSAGE_ID")
	}
	return r.mutate(http.MethodPost, "/v1/messages/"+url.PathEscape(args[0])+suffix, map[string]any{}, true)
}
func (r *runner) thread(args []string) error {
	if len(args) == 0 {
		return usage("thread requires MESSAGE_ID")
	}
	q, e := listQuery("thread", args[1:], false)
	if e != nil {
		return e
	}
	return r.get("/v1/messages/"+url.PathEscape(args[0])+"/thread", q, false)
}
func (r *runner) search(args []string) error {
	if len(args) == 0 {
		return usage("search requires QUERY")
	}
	fs := newFlagSet("search")
	from := fs.String("from", "", "")
	topic := fs.String("topic", "", "")
	limit := fs.Int("limit", 0, "")
	cursor := fs.String("cursor", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() != 0 {
		return usage("unexpected search arguments")
	}
	q := url.Values{"query": {args[0]}}
	set(q, "from", *from)
	set(q, "topic", *topic)
	setInt(q, "limit", *limit)
	set(q, "cursor", *cursor)
	return r.get("/v1/search", q, false)
}
func (r *runner) observe(args []string) error {
	q, e := listQuery("observe", args, false)
	if e != nil {
		return e
	}
	return r.get("/v1/observe", q, false)
}
func (r *runner) purge(args []string) error {
	fs := newFlagSet("purge")
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() != 0 {
		return usage("unexpected purge arguments")
	}
	return r.mutate(http.MethodPost, "/v1/purge", map[string]any{"dry_run": *dry}, false)
}
func (r *runner) export(args []string) error {
	fs := newFlagSet("export")
	output := fs.String("output", "", "")
	if err := fs.Parse(args); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() != 0 {
		return usage("unexpected export arguments")
	}
	client, e := r.client(false)
	if e != nil {
		return e
	}
	ctx, cancel := r.callContext()
	defer cancel()
	if *output == "" {
		return client.Export(ctx, r.env.Stdout)
	}
	directory := filepath.Dir(*output)
	temporary, e := os.CreateTemp(directory, ".comms-export-*.jsonl")
	if e != nil {
		return e
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if e = temporary.Chmod(0o600); e == nil {
		e = client.Export(ctx, temporary)
	}
	if e == nil {
		e = temporary.Sync()
	}
	closeErr := temporary.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporaryPath, *output)
}

func (r *runner) get(path string, q url.Values, identity bool) error {
	client, e := r.client(identity)
	if e != nil {
		return e
	}
	var value any
	if e = r.do(client, http.MethodGet, path, q, nil, &value); e != nil {
		return e
	}
	return r.output(value)
}
func (r *runner) mutate(method, path string, body map[string]any, identity bool) error {
	selected, client, e := r.clientWithIdentity(identity)
	if e != nil {
		return e
	}
	clientID := selected.Record.ClientID
	if clientID == "" {
		clientID, e = newClientID()
		if e != nil {
			return e
		}
	}
	requestID, e := newRequestID()
	if e != nil {
		return e
	}
	body["client_id"] = clientID
	body["request_id"] = requestID
	var value any
	if e = r.do(client, method, path, nil, body, &value); e != nil {
		return e
	}
	return r.output(value)
}
func (r *runner) client(required bool) (*httpapi.Client, error) {
	_, client, e := r.clientWithIdentity(required)
	return client, e
}
func (r *runner) identityClient() (selectedIdentity, *httpapi.Client, error) {
	return r.clientWithIdentity(true)
}
func (r *runner) clientWithIdentity(required bool) (selectedIdentity, *httpapi.Client, error) {
	var selected selectedIdentity
	var e error
	switch {
	case required:
		selected, e = resolveIdentity(r.g.as, r.env.Getenv)
		if e != nil {
			return selected, nil, usage(e.Error())
		}
	case r.g.as != "":
		selected = selectedIdentity{Agent: r.g.as, Source: "--as"}
	default:
		selected, _ = resolveIdentity("", r.env.Getenv)
	}
	socket := r.g.socket
	if socket == "" {
		socket = r.env.Getenv("COMMS_SOCKET")
	}
	if socket == "" {
		socket, e = service.ResolveSocketPath(false)
		if e != nil {
			return selected, nil, e
		}
	}
	return selected, httpapi.NewUnixClient(socket, selected.Agent), nil
}
func (r *runner) callContext() (context.Context, context.CancelFunc) {
	if r.g.timeout <= 0 {
		return r.env.Context, func() {}
	}
	return context.WithTimeout(r.env.Context, r.g.timeout)
}

func (r *runner) do(client *httpapi.Client, method, path string, query url.Values, input, output any) error {
	ctx, cancel := r.callContext()
	defer cancel()
	return client.Do(ctx, method, path, query, input, output)
}

func (r *runner) output(value any) error {
	if r.g.json {
		return json.NewEncoder(r.env.Stdout).Encode(map[string]any{"schema": cliSchema, "data": value})
	}
	return renderHuman(r.env.Stdout, value)
}

func renderHuman(w io.Writer, value any) error {
	switch v := value.(type) {
	case map[string]any:
		if items, ok := v["items"].([]any); ok {
			for _, item := range items {
				if err := renderHuman(w, item); err != nil {
					return err
				}
			}
			if cursor, _ := v["next_cursor"].(string); cursor != "" {
				_, _ = fmt.Fprintf(w, "next cursor: %s\n", cursor)
			}
			return nil
		}
		if agent, ok := v["agent"].(map[string]any); ok {
			if err := renderHuman(w, agent); err != nil {
				return err
			}
			for _, key := range []string{"source", "identity_source", "context"} {
				if text, _ := v[key].(string); text != "" {
					_, _ = fmt.Fprintf(w, "%s: %s\n", strings.ReplaceAll(key, "_", " "), text)
				}
			}
			return nil
		}
		if id, _ := v["id"].(string); id != "" {
			if handle, _ := v["handle"].(string); handle != "" {
				_, err := fmt.Fprintf(w, "@%s\t%s\n", handle, id)
				return err
			}
			if body, ok := v["body"].(string); ok {
				title, _ := v["title"].(string)
				sequence := v["sequence"]
				_, err := fmt.Fprintf(w, "%s\t#%v\t%s\n%s\n", id, sequence, title, body)
				return err
			}
			if name, _ := v["name"].(string); name != "" {
				_, err := fmt.Fprintf(w, "%s\t%s\n", name, id)
				return err
			}
		}
		if through, ok := v["new_sequence"]; ok {
			_, err := fmt.Fprintf(w, "read through %v (was %v; acknowledged %v)\n", through, v["previous_sequence"], v["newly_acknowledged"])
			return err
		}
		if topic, ok := v["topic_id"].(string); ok {
			_, err := fmt.Fprintf(w, "%s\t%s\tread-through=%v\n", v["agent_id"], topic, v["read_through_sequence"])
			return err
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			raw, err := json.Marshal(v[key])
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(w, "%s: %s\n", key, raw)
		}
		return nil
	case []any:
		for _, item := range v {
			if err := renderHuman(w, item); err != nil {
				return err
			}
		}
		return nil
	default:
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", data)
		return err
	}
}

func parseGlobals(args []string) (globals, []string, error) {
	g := globals{timeout: 20 * time.Second}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			g.json = true
		case arg == "--help" || arg == "-h":
			g.help = true
		case arg == "--as":
			i++
			if i >= len(args) {
				return g, nil, usage("--as requires a value")
			}
			g.as = args[i]
		case strings.HasPrefix(arg, "--as="):
			g.as = strings.TrimPrefix(arg, "--as=")
		case arg == "--socket":
			i++
			if i >= len(args) {
				return g, nil, usage("--socket requires a value")
			}
			g.socket = args[i]
		case strings.HasPrefix(arg, "--socket="):
			g.socket = strings.TrimPrefix(arg, "--socket=")
		case arg == "--timeout":
			i++
			if i >= len(args) {
				return g, nil, usage("--timeout requires a value")
			}
			d, e := time.ParseDuration(args[i])
			if e != nil {
				return g, nil, usage("invalid --timeout")
			}
			g.timeout = d
		case strings.HasPrefix(arg, "--timeout="):
			d, e := time.ParseDuration(strings.TrimPrefix(arg, "--timeout="))
			if e != nil {
				return g, nil, usage("invalid --timeout")
			}
			g.timeout = d
		default:
			rest = append(rest, arg)
		}
	}
	return g, rest, nil
}
func listQuery(name string, args []string, all bool) (url.Values, error) {
	fs := newFlagSet(name)
	limit := fs.Int("limit", 0, "")
	cursor := fs.String("cursor", "", "")
	var includeAll *bool
	if all {
		includeAll = fs.Bool("all", false, "")
	}
	if e := fs.Parse(args); e != nil {
		return nil, usage(e.Error())
	}
	if fs.NArg() != 0 {
		return nil, usage("unexpected " + name + " arguments")
	}
	q := url.Values{}
	setInt(q, "limit", *limit)
	set(q, "cursor", *cursor)
	if includeAll != nil {
		setBool(q, "all", *includeAll)
	}
	return q, nil
}
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
func wasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
func set(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}
func setInt(q url.Values, key string, value int) {
	if value != 0 {
		q.Set(key, strconv.Itoa(value))
	}
}
func setBool(q url.Values, key string, value bool) {
	if value {
		q.Set(key, "true")
	}
}

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }
func usage(message string) error   { return usageError{message: message} }
func fail(env Env, jsonOutput bool, err error) int {
	code := 1
	var ue usageError
	switch {
	case errors.As(err, &ue), errors.Is(err, domain.ErrInvalid):
		code = 2
	case errors.Is(err, app.ErrNotFound):
		code = 3
	case errors.Is(err, app.ErrConflict), errors.Is(err, httpapi.ErrServerInstanceChanged):
		code = 4
	case errors.Is(err, app.ErrUnavailable), errors.Is(err, app.ErrOverloaded), errors.Is(err, app.ErrClosed), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = 5
	}
	if jsonOutput {
		stable := "internal"
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			stable = "timeout"
		case errors.Is(err, httpapi.ErrServerInstanceChanged):
			stable = "server_instance_changed"
		case code == 2:
			stable = "invalid_argument"
		case code == 3:
			stable = "not_found"
		case code == 4:
			stable = "conflict"
		case code == 5:
			stable = "unavailable"
		}
		_ = json.NewEncoder(env.Stderr).Encode(httpapi.ErrorEnvelope{Error: httpapi.ErrorBody{Code: stable, Message: err.Error(), Details: map[string]any{}}})
	} else {
		_, _ = fmt.Fprintf(env.Stderr, "comms: %v\n", err)
		if code == 5 {
			_, _ = io.WriteString(env.Stderr, "Start the service with 'comms serve'.\n")
		}
	}
	return code
}
