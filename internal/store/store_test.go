package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type testSystem struct {
	adapter *Adapter
	service *app.Service
	clock   *testClock
	path    string
}

func newTestSystem(t *testing.T) *testSystem {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comms.db")
	a, e := Open(context.Background(), Options{Path: path, QueueDepth: 128})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = a.Close() })
	clock := &testClock{now: time.Date(2026, 9, 4, 12, 0, 0, 123456000, time.UTC)}
	return &testSystem{adapter: a, service: app.NewService(a, clock), clock: clock, path: path}
}
func join(t *testing.T, s *app.Service, handle string, ref *app.ExternalRef) domain.Agent {
	t.Helper()
	got, e := s.Join(context.Background(), app.JoinRequest{Handle: handle, ExternalRef: ref})
	if e != nil {
		t.Fatal(e)
	}
	return got.Agent
}
func createTopic(t *testing.T, s *app.Service, name string) domain.Topic {
	t.Helper()
	got, e := s.CreateTopic(context.Background(), app.CreateTopicRequest{Name: name})
	if e != nil {
		t.Fatal(e)
	}
	return got
}
func follow(t *testing.T, s *app.Service, agent domain.Agent, topic domain.Topic) {
	t.Helper()
	if _, e := s.Follow(context.Background(), app.FollowRequest{Agent: string(agent.ID), Topic: string(topic.ID)}); e != nil {
		t.Fatal(e)
	}
}

func TestConversationReceiptsAndIdempotency(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	req := app.PublishRequest{Mutation: app.Mutation{ClientID: "alice-client", RequestID: "publish-1"}, Author: "alice", Topic: "build", Title: "status", Body: "steel thread is green"}
	root, e := sys.service.Publish(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	again, e := sys.service.Publish(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if again.ID != root.ID || again.Sequence != root.Sequence {
		t.Fatalf("idempotent publish changed result: %#v %#v", root, again)
	}
	box, e := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "bob", UnreadOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	if len(box.Items) != 1 || box.Items[0].ID != root.ID {
		t.Fatalf("inbox=%#v", box)
	}
	if _, e = sys.service.Peek(ctx, string(root.ID)); e != nil {
		t.Fatal(e)
	}
	box, e = sys.service.Inbox(ctx, app.MessageListRequest{Agent: "bob", UnreadOnly: true})
	if e != nil || len(box.Items) != 1 {
		t.Fatalf("peek mutated cursor: %#v %v", box, e)
	}
	read, e := sys.service.ReadThrough(ctx, app.ReadThroughRequest{Agent: "bob", Message: string(root.ID)})
	if e != nil {
		t.Fatal(e)
	}
	if read.PreviousSequence != 0 || read.NewSequence != 1 || read.NewlyAcknowledged != 1 {
		t.Fatalf("read result=%#v", read)
	}
	receipts, e := sys.service.Receipts(ctx, string(root.ID))
	if e != nil {
		t.Fatal(e)
	}
	if len(receipts) != 1 || receipts[0].Agent.ID != bob.ID || receipts[0].State != "read" || receipts[0].ReadAt == nil {
		t.Fatalf("receipts=%#v", receipts)
	}
	reply, e := sys.service.Reply(ctx, app.ReplyRequest{Author: "bob", Parent: string(root.ID), Body: "confirmed"})
	if e != nil {
		t.Fatal(e)
	}
	if reply.Sequence != 2 || reply.ThreadRootID != root.ID || reply.InReplyTo == nil {
		t.Fatalf("reply=%#v", reply)
	}
	thread, e := sys.service.Thread(ctx, app.ThreadRequest{Message: string(reply.ID)})
	if e != nil {
		t.Fatal(e)
	}
	if len(thread.Items) != 2 {
		t.Fatalf("thread=%#v", thread)
	}
	if _, e = sys.service.Follow(ctx, app.FollowRequest{Mutation: req.Mutation, Agent: "alice", Topic: "build"}); !errors.Is(e, app.ErrConflict) {
		t.Fatalf("cross-operation idempotency error=%v", e)
	}
}

func TestIdentityTopicsAndDirectRouting(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	ref := app.ExternalRef{Namespace: "test", Key: "session-a"}
	alice := join(t, sys.service, "alice", &ref)
	bob := join(t, sys.service, "bob", nil)
	eve := join(t, sys.service, "eve", nil)
	rejoined, e := sys.service.Join(ctx, app.JoinRequest{Handle: "ignored", Harness: "codex", ExternalRef: &ref})
	if e != nil {
		t.Fatal(e)
	}
	if !rejoined.Rejoined || rejoined.Agent.ID != alice.ID || rejoined.Agent.Harness != "codex" {
		t.Fatalf("rejoin=%#v", rejoined)
	}
	newHandle := "alice-renamed"
	if _, e = sys.service.UpdateAgent(ctx, app.UpdateAgentRequest{Agent: "alice", Handle: &newHandle}); e != nil {
		t.Fatal(e)
	}
	viaAlias, e := sys.service.GetAgent(ctx, "alice", false)
	if e != nil || viaAlias.ID != alice.ID {
		t.Fatalf("alias resolve=%#v %v", viaAlias, e)
	}
	topic := createTopic(t, sys.service, "project")
	ens, e := sys.service.EnsureTopic(ctx, app.EnsureTopicRequest{ExternalRef: app.ExternalRef{Namespace: "sidecar", Key: "p1"}, Name: "project"})
	if e != nil {
		t.Fatal(e)
	}
	if !ens.Created || ens.Topic.Name != "project-2" {
		t.Fatalf("ensured=%#v", ens)
	}
	again, e := sys.service.EnsureTopic(ctx, app.EnsureTopicRequest{ExternalRef: app.ExternalRef{Namespace: "sidecar", Key: "p1"}, Name: "different"})
	if e != nil || again.Created || again.Topic.ID != ens.Topic.ID {
		t.Fatalf("ensure again=%#v %v", again, e)
	}
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	_ = eve
	direct, e := sys.service.DirectSend(ctx, app.DirectSendRequest{Author: string(alice.ID), Recipient: "bob", Title: "private routing only", Body: "deliberately observable needle"})
	if e != nil {
		t.Fatal(e)
	}
	eves, e := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "eve"})
	if e != nil {
		t.Fatal(e)
	}
	if len(eves.Items) != 0 {
		t.Fatalf("direct leaked to inbox: %#v", eves)
	}
	topics, e := sys.service.Topics(ctx, string(eve.ID), app.TopicListRequest{IncludeDirect: true})
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range topics.Items {
		if v.ID == direct.TopicID {
			t.Fatalf("direct topic discovered by unrelated agent")
		}
	}
	observed, e := sys.service.Observe(ctx, app.ObserveRequest{})
	if e != nil {
		t.Fatal(e)
	}
	if len(observed.Items) != 1 || observed.Items[0].ID != direct.ID {
		t.Fatalf("observe=%#v", observed)
	}
	found, e := sys.service.Search(ctx, app.SearchRequest{Query: "needle"})
	if e != nil {
		t.Fatal(e)
	}
	if len(found.Items) != 1 || found.Items[0].ID != direct.ID {
		t.Fatalf("search=%#v", found)
	}
	if _, e = sys.service.Peek(ctx, string(direct.ID)); e != nil {
		t.Fatal(e)
	}
}

func TestRecentContextRefollowAliasExpiryAndFormerReceipt(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	carol := join(t, sys.service, "carol", nil)
	topic := createTopic(t, sys.service, "history")
	follow(t, sys.service, alice, topic)
	first, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "history", Title: "one", Body: "body"})
	if e != nil {
		t.Fatal(e)
	}
	sys.clock.Advance(time.Microsecond)
	second, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "history", Title: "two", Body: "body"})
	if e != nil {
		t.Fatal(e)
	}
	sys.clock.Advance(time.Microsecond)
	follow(t, sys.service, bob, topic)
	box, e := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "bob", UnreadOnly: true})
	if e != nil || len(box.Items) != 2 {
		t.Fatalf("new subscriber context=%#v %v", box, e)
	}
	if _, e = sys.service.ReadThrough(ctx, app.ReadThroughRequest{Agent: "bob", Message: string(first.ID)}); e != nil {
		t.Fatal(e)
	}
	if _, e = sys.service.Unfollow(ctx, app.UnfollowRequest{Agent: "bob", Topic: "history"}); e != nil {
		t.Fatal(e)
	}
	follow(t, sys.service, carol, topic)
	receipts, e := sys.service.Receipts(ctx, string(first.ID))
	if e != nil {
		t.Fatal(e)
	}
	if len(receipts) != 2 || receipts[0].Agent.ID != bob.ID || receipts[0].State != "read" || receipts[1].Agent.ID != carol.ID || receipts[1].State != "unread" {
		t.Fatalf("active/former receipts=%#v", receipts)
	}
	third, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "history", Title: "three", Body: "body"})
	if e != nil {
		t.Fatal(e)
	}
	follow(t, sys.service, bob, topic)
	box, e = sys.service.Inbox(ctx, app.MessageListRequest{Agent: "bob", UnreadOnly: true})
	if e != nil || len(box.Items) != 2 || box.Items[0].ID != third.ID || box.Items[1].ID != second.ID {
		t.Fatalf("refollow did not preserve cursor: %#v %v", box, e)
	}
	newHandle := "alice-new"
	if _, e = sys.service.UpdateAgent(ctx, app.UpdateAgentRequest{Agent: "alice", Handle: &newHandle}); e != nil {
		t.Fatal(e)
	}
	if _, e = sys.service.GetAgent(ctx, "alice", false); e != nil {
		t.Fatalf("live alias did not resolve: %v", e)
	}
	sys.clock.Advance(domain.AliasLifetime + time.Microsecond)
	if _, e = sys.service.GetAgent(ctx, "alice", false); !errors.Is(e, app.ErrNotFound) {
		t.Fatalf("expired alias error=%v", e)
	}
	if got := join(t, sys.service, "alice", nil); got.ID == alice.ID {
		t.Fatal("expired alias reuse did not create a new identity")
	}
}

func TestRetentionPreservesLiveThreadContext(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	topic := createTopic(t, sys.service, "retention")
	follow(t, sys.service, alice, topic)
	root, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "retention", Title: "root", Body: "old", Expiry: app.Expiry{After: time.Hour}})
	if e != nil {
		t.Fatal(e)
	}
	reply, e := sys.service.Reply(ctx, app.ReplyRequest{Author: "alice", Parent: string(root.ID), Body: "still live", Expiry: app.Expiry{After: 3 * time.Hour}})
	if e != nil {
		t.Fatal(e)
	}
	sys.clock.Advance(2 * time.Hour)
	thread, e := sys.service.Thread(ctx, app.ThreadRequest{Message: string(reply.ID)})
	if e != nil || len(thread.Items) != 2 {
		t.Fatalf("live thread lost context: %#v %v", thread, e)
	}
	status, e := sys.service.RetentionStatus(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if status.ExpiredMessages != 1 || status.PurgeableMessages != 0 {
		t.Fatalf("status=%#v", status)
	}
	run, e := sys.service.Purge(ctx, app.PurgeRequest{})
	if e != nil || run.RemovedMessages != 0 {
		t.Fatalf("early purge=%#v %v", run, e)
	}
	sys.clock.Advance(2 * time.Hour)
	run, e = sys.service.Purge(ctx, app.PurgeRequest{})
	if e != nil || run.RemovedMessages != 2 {
		t.Fatalf("final purge=%#v %v", run, e)
	}
	if _, e = sys.service.Peek(ctx, string(root.ID)); !errors.Is(e, app.ErrNotFound) {
		t.Fatalf("purged root error=%v", e)
	}
}

func TestConcurrentPublishingIsGapFreeAndObserveDoesNotRead(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	topic := createTopic(t, sys.service, "parallel")
	follow(t, sys.service, alice, topic)
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	readerDone := make(chan struct{})
	readerErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-readerDone:
				readerErr <- nil
				return
			default:
				if _, e := sys.service.Observe(ctx, app.ObserveRequest{}); e != nil {
					readerErr <- e
					return
				}
			}
		}
	}()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "parallel", Title: fmt.Sprintf("m-%02d", i), Body: "body"})
			errs <- e
		}(i)
	}
	wg.Wait()
	close(readerDone)
	if e := <-readerErr; e != nil {
		t.Fatalf("concurrent reader: %v", e)
	}
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	page, e := sys.service.TopicMessages(ctx, app.MessageListRequest{Topic: "parallel", PageRequest: app.PageRequest{Limit: 100}})
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Items) != n {
		t.Fatalf("messages=%d", len(page.Items))
	}
	seq := make([]int, len(page.Items))
	for i, m := range page.Items {
		seq[i] = int(m.Sequence)
	}
	sort.Ints(seq)
	for i, v := range seq {
		if v != i+1 {
			t.Fatalf("sequences=%v", seq)
		}
	}
	before, e := sys.service.Subscriptions(ctx, app.SubscriptionListRequest{Agent: string(alice.ID)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = sys.service.Observe(ctx, app.ObserveRequest{PageRequest: app.PageRequest{Limit: 100}}); e != nil {
		t.Fatal(e)
	}
	after, e := sys.service.Subscriptions(ctx, app.SubscriptionListRequest{Agent: string(alice.ID)})
	if e != nil {
		t.Fatal(e)
	}
	if before.Items[0].ReadThroughSequence != after.Items[0].ReadThroughSequence || after.Items[0].ReadThroughAt != nil {
		t.Fatalf("observe changed cursor: %#v %#v", before, after)
	}
}

func TestRollbackDoesNotConsumeSequenceOrLeaveDirectTopic(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "atomic")
	follow(t, sys.service, alice, topic)
	first, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "atomic", Title: "first", Body: "body"})
	if e != nil {
		t.Fatal(e)
	}
	bad := app.PreparedMessage{ID: first.ID, Author: "alice", Recipient: "bob", Title: "duplicate", Body: "body", ExpiresAt: domain.DefaultExpiry(sys.clock.Now()), Now: sys.clock.Now()}
	if _, e = sys.adapter.DirectSend(ctx, bad); !errors.Is(e, app.ErrConflict) {
		t.Fatalf("failed direct send error=%v", e)
	}
	snapshot, e := sys.service.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(snapshot.Topics) != 1 || len(snapshot.Subscriptions) != 1 {
		t.Fatalf("partial direct topic creation: topics=%d subscriptions=%d", len(snapshot.Topics), len(snapshot.Subscriptions))
	}
	second, e := sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "atomic", Title: "second", Body: "body"})
	if e != nil {
		t.Fatal(e)
	}
	if second.Sequence != 2 {
		t.Fatalf("rollback consumed sequence: %d", second.Sequence)
	}
	_ = bob
}

func TestDirectTopicsCannotBePreclaimedLeakedOrGenericallyArchived(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	eve := join(t, sys.service, "eve", nil)
	ref := domain.DirectExternalRef(alice.ID, bob.ID)
	if _, err := sys.service.EnsureTopic(ctx, app.EnsureTopicRequest{ExternalRef: ref, Name: "poison"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("reserved ensure error=%v", err)
	}

	poison := createTopic(t, sys.service, "poison")
	follow(t, sys.service, alice, poison)
	follow(t, sys.service, bob, poison)
	follow(t, sys.service, eve, poison)
	_, err := sys.adapter.submit(ctx, func(conn *sql.Conn) (any, error) {
		_, err := conn.ExecContext(ctx, "INSERT INTO topic_external_refs(namespace,external_key,topic_id) VALUES(?,?,?)", ref.Namespace, ref.Key, poison.ID)
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sys.service.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipient: "bob", Title: "secret", Body: "body"}); !errors.Is(err, app.ErrConflict) {
		t.Fatalf("poisoned direct send error=%v", err)
	}
	box, err := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "eve"})
	if err != nil {
		t.Fatal(err)
	}
	if len(box.Items) != 0 {
		t.Fatalf("poisoned direct leaked: %#v", box.Items)
	}

	clean := newTestSystem(t)
	alice = join(t, clean.service, "alice", nil)
	bob = join(t, clean.service, "bob", nil)
	ref = domain.DirectExternalRef(alice.ID, bob.ID)
	_ = createTopic(t, clean.service, "direct:"+ref.Key)
	direct, err := clean.service.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipient: "bob", Title: "safe", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := clean.service.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var directTopic domain.Topic
	for _, topic := range snapshot.Topics {
		if topic.ID == direct.TopicID {
			directTopic = topic
		}
	}
	if directTopic.Kind != domain.TopicDirect || directTopic.Name == "direct:"+ref.Key {
		t.Fatalf("direct collision fallback=%#v", directTopic)
	}
	if _, err = clean.service.ArchiveTopic(ctx, app.ArchiveTopicRequest{Topic: string(direct.TopicID)}); !errors.Is(err, app.ErrConflict) {
		t.Fatalf("direct archive error=%v", err)
	}
}

func TestUnicodeTopicIdentityAndLiteralSearch(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	_ = createTopic(t, sys.service, "CAFÉ")
	if _, err := sys.service.CreateTopic(ctx, app.CreateTopicRequest{Name: "café"}); !errors.Is(err, app.ErrConflict) {
		t.Fatalf("Unicode case collision error=%v", err)
	}
	long := strings.Repeat("é", 64)
	_ = createTopic(t, sys.service, long)
	ensured, err := sys.service.EnsureTopic(ctx, app.EnsureTopicRequest{ExternalRef: app.ExternalRef{Namespace: "sidecar", Key: "unicode"}, Name: long})
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(ensured.Topic.Name) || len(ensured.Topic.Name) > domain.MaxTopicName {
		t.Fatalf("invalid suffixed topic %q", ensured.Topic.Name)
	}

	alice := join(t, sys.service, "alice", nil)
	topic := createTopic(t, sys.service, "search")
	follow(t, sys.service, alice, topic)
	if _, err = sys.service.Publish(ctx, app.PublishRequest{Author: "alice", Topic: "search", Title: "hello-world", Body: `a quoted " phrase`}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"hello-world", `"`} {
		if _, err = sys.service.Search(ctx, app.SearchRequest{Query: query}); err != nil {
			t.Fatalf("literal search %q: %v", query, err)
		}
	}
}

func TestBoundedWriterQueueAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comms.db")
	a, e := Open(context.Background(), Options{Path: path, QueueDepth: 1})
	if e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, e := a.submit(context.Background(), func(*sql.Conn) (any, error) {
			close(started)
			<-release
			return nil, nil
		})
		first <- e
	}()
	<-started
	queued := writeRequest{ctx: context.Background(), fn: func(*sql.Conn) (any, error) { return nil, nil }, result: make(chan writeResult, 1)}
	a.requests <- queued
	if _, e = a.submit(context.Background(), func(*sql.Conn) (any, error) { return nil, nil }); !errors.Is(e, app.ErrOverloaded) {
		t.Fatalf("overload error=%v", e)
	}
	close(release)
	if e = <-first; e != nil {
		t.Fatal(e)
	}
	if result := <-queued.result; result.err != nil {
		t.Fatal(result.err)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = a.submit(context.Background(), func(*sql.Conn) (any, error) { return nil, nil }); !errors.Is(e, app.ErrClosed) {
		t.Fatalf("closed error=%v", e)
	}
}

func TestOwnerReopenFutureSchemaAndCancellation(t *testing.T) {
	sys := newTestSystem(t)
	if _, e := Open(context.Background(), Options{Path: sys.path}); !errors.Is(e, app.ErrUnavailable) {
		t.Fatalf("second owner error=%v", e)
	}
	if e := sys.adapter.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(context.Background(), Options{Path: sys.path})
	if e != nil {
		t.Fatal(e)
	}
	handshake, e := reopened.Handshake(context.Background())
	if e != nil || handshake.StoreID == "" {
		t.Fatalf("handshake=%#v %v", handshake, e)
	}
	if e = reopened.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", sqliteDSN(sys.path, false))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO schema_migrations(version,applied_at) VALUES(99,0)"); e != nil {
		t.Fatal(e)
	}
	_ = db.Close()
	if _, e = Open(context.Background(), Options{Path: sys.path}); !errors.Is(e, app.ErrConflict) {
		t.Fatalf("future schema error=%v", e)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	fresh := newTestSystem(t)
	if _, e = fresh.service.Join(cancelled, app.JoinRequest{Handle: "cancelled"}); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel error=%v", e)
	}
	if _, e = fresh.service.GetAgent(context.Background(), "cancelled", false); !errors.Is(e, app.ErrNotFound) {
		t.Fatalf("cancelled mutation became visible: %v", e)
	}
}

func TestResolveStatePathReadOnlyDoesNotCreate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "absent")
	t.Setenv("COMMS_STATE_DIR", stateDir)
	path, e := ResolveStatePath(false)
	if e != nil {
		t.Fatal(e)
	}
	if path != filepath.Join(stateDir, "comms.db") {
		t.Fatalf("path=%q", path)
	}
	if _, e = os.Stat(stateDir); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("diagnostic resolution created state directory: %v", e)
	}
}

// publish is a focused helper for the inbox-projection and wait tests, which
// care about authorship and ordering rather than idempotency.
func publish(t *testing.T, sys *testSystem, author, topic, title string) domain.Message {
	t.Helper()
	sys.clock.Advance(time.Millisecond)
	m, e := sys.service.Publish(context.Background(), app.PublishRequest{Author: author, Topic: topic, Title: title, Body: title})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func replyTo(t *testing.T, sys *testSystem, author string, parent domain.Message, title string) domain.Message {
	t.Helper()
	sys.clock.Advance(time.Millisecond)
	m, e := sys.service.Reply(context.Background(), app.ReplyRequest{Author: author, Parent: string(parent.ID), Title: title, Body: title})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func directSend(t *testing.T, sys *testSystem, author, recipient, title string) domain.Message {
	t.Helper()
	sys.clock.Advance(time.Millisecond)
	m, e := sys.service.DirectSend(context.Background(), app.DirectSendRequest{Author: author, Recipient: recipient, Title: title, Body: title})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func inboxTitles(t *testing.T, sys *testSystem, req app.MessageListRequest) []string {
	t.Helper()
	page, e := sys.service.Inbox(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	titles := make([]string, 0, len(page.Items))
	for _, m := range page.Items {
		titles = append(titles, m.Title)
	}
	return titles
}
func equalTitles(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestInboxExcludesSelfAuthoredMessagesByDefault(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)

	outbound := directSend(t, sys, "alice", "bob", "alice-direct")
	answer := replyTo(t, sys, "bob", outbound, "bob-direct-reply")
	mine := publish(t, sys, "alice", "build", "alice-topic-post")
	theirs := publish(t, sys, "bob", "build", "bob-topic-post")

	for _, unread := range []bool{false, true} {
		got := inboxTitles(t, sys, app.MessageListRequest{Agent: "alice", UnreadOnly: unread})
		if !equalTitles(got, []string{"bob-topic-post", "bob-direct-reply"}) {
			t.Fatalf("alice inbox (unread=%v) = %v; want only bob's traffic", unread, got)
		}
	}
	page, e := sys.service.Inbox(ctx, app.MessageListRequest{Agent: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	if page.Items[1].ID != answer.ID || page.Items[1].AuthorID != bob.ID {
		t.Fatalf("alice inbox second item = %#v; want bob's reply", page.Items[1])
	}

	// The recipient still sees the incoming direct message.
	if got := inboxTitles(t, sys, app.MessageListRequest{Agent: "bob"}); !equalTitles(got, []string{"alice-topic-post", "alice-direct"}) {
		t.Fatalf("bob inbox = %v", got)
	}

	// The opt-in restores the sender's own messages.
	got := inboxTitles(t, sys, app.MessageListRequest{Agent: "alice", IncludeSelf: true})
	if !equalTitles(got, []string{"bob-topic-post", "alice-topic-post", "bob-direct-reply", "alice-direct"}) {
		t.Fatalf("alice inbox with IncludeSelf = %v", got)
	}
	if theirs.ID == mine.ID {
		t.Fatal("test fixture is degenerate")
	}

	// Exclusion is a projection: nothing was deleted and every other surface
	// still shows the sender's own messages.
	for name, list := range map[string]func() (app.Page[domain.Message], error){
		"topic": func() (app.Page[domain.Message], error) {
			return sys.service.TopicMessages(ctx, app.MessageListRequest{Topic: "build"})
		},
		"thread": func() (app.Page[domain.Message], error) {
			return sys.service.Thread(ctx, app.ThreadRequest{Message: string(outbound.ID)})
		},
		"search": func() (app.Page[domain.Message], error) {
			return sys.service.Search(ctx, app.SearchRequest{Query: "alice-direct"})
		},
		"observe": func() (app.Page[domain.Message], error) {
			return sys.service.Observe(ctx, app.ObserveRequest{})
		},
	} {
		page, e := list()
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		found := false
		for _, m := range page.Items {
			if m.AuthorID == alice.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s dropped alice's own messages: %#v", name, page.Items)
		}
	}
	if _, e := sys.service.Peek(ctx, string(mine.ID)); e != nil {
		t.Fatalf("peek on own message: %v", e)
	}
}

func TestInboxThreadSummariesSurfaceRepliesInSelfStartedThreads(t *testing.T) {
	sys := newTestSystem(t)
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)

	mine := publish(t, sys, "alice", "build", "alice-root")
	reply := replyTo(t, sys, "bob", mine, "bob-reply")
	theirs := publish(t, sys, "bob", "build", "bob-root")
	replyTo(t, sys, "bob", theirs, "bob-followup")

	// Alice cannot see her own root, so the summary of her thread becomes the
	// earliest incoming reply rather than nothing at all.
	got := inboxTitles(t, sys, app.MessageListRequest{Agent: "alice", ThreadsOnly: true})
	if !equalTitles(got, []string{"bob-root", "bob-reply"}) {
		t.Fatalf("alice thread summaries = %v", got)
	}
	page, e := sys.service.Inbox(context.Background(), app.MessageListRequest{Agent: "alice", ThreadsOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	if page.Items[1].ID != reply.ID {
		t.Fatalf("summary of alice's own thread = %s, want reply %s", page.Items[1].ID, reply.ID)
	}
	// With her own messages included the summary is the structural root again.
	got = inboxTitles(t, sys, app.MessageListRequest{Agent: "alice", ThreadsOnly: true, IncludeSelf: true})
	if !equalTitles(got, []string{"bob-root", "alice-root"}) {
		t.Fatalf("alice thread summaries with IncludeSelf = %v", got)
	}
	// Bob, who wrote the replies, sees only alice's root collapsed.
	if got := inboxTitles(t, sys, app.MessageListRequest{Agent: "bob", ThreadsOnly: true}); !equalTitles(got, []string{"alice-root"}) {
		t.Fatalf("bob thread summaries = %v", got)
	}
}

func TestInboxSelfExclusionPagesCompletelyAndDoesNotAcknowledge(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)

	// Surround every incoming message with several self-authored ones so a
	// naive post-query filter would return short or empty pages.
	want := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		for j := 0; j < 3; j++ {
			publish(t, sys, "alice", "build", fmt.Sprintf("alice-%d-%d", i, j))
		}
		title := fmt.Sprintf("bob-%d", i)
		publish(t, sys, "bob", "build", title)
		want = append([]string{title}, want...)
	}

	got := []string{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		page, e := sys.service.Inbox(ctx, app.MessageListRequest{PageRequest: app.PageRequest{Limit: 2, Cursor: cursor}, Agent: "alice", UnreadOnly: true})
		if e != nil {
			t.Fatal(e)
		}
		if len(page.Items) > 2 {
			t.Fatalf("page exceeded limit: %d", len(page.Items))
		}
		for _, m := range page.Items {
			if m.AuthorID == alice.ID {
				t.Fatalf("self-authored message %q survived paging", m.Title)
			}
			got = append(got, m.Title)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if !equalTitles(got, want) {
		t.Fatalf("paged inbox = %v, want %v", got, want)
	}

	// Filtering must not move a cursor or invent acknowledgements.
	subscriptions, e := sys.service.Subscriptions(ctx, app.SubscriptionListRequest{Agent: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	if len(subscriptions.Items) != 1 || subscriptions.Items[0].ReadThroughSequence != 0 || subscriptions.Items[0].ReadThroughAt != nil {
		t.Fatalf("inbox listing advanced a cursor: %#v", subscriptions.Items)
	}
	// read-through still acknowledges every earlier visible message in the
	// topic, including the reader's own.
	last := publish(t, sys, "bob", "build", "bob-final")
	read, e := sys.service.ReadThrough(ctx, app.ReadThroughRequest{Agent: "alice", Message: string(last.ID)})
	if e != nil {
		t.Fatal(e)
	}
	if read.NewlyAcknowledged != 25 {
		t.Fatalf("read-through acknowledged %d, want every visible message", read.NewlyAcknowledged)
	}
	if got := inboxTitles(t, sys, app.MessageListRequest{Agent: "alice", UnreadOnly: true}); len(got) != 0 {
		t.Fatalf("unread after read-through = %v", got)
	}
	_ = bob
}

func TestInboxSelfExclusionComparesIdentityNotHandle(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)
	publish(t, sys, "alice", "build", "before-rename")
	publish(t, sys, "bob", "build", "incoming")

	renamed := "alice-two"
	if _, e := sys.service.UpdateAgent(ctx, app.UpdateAgentRequest{Agent: string(alice.ID), Handle: &renamed}); e != nil {
		t.Fatal(e)
	}
	// The message was written under the old handle; identity is what matters.
	if got := inboxTitles(t, sys, app.MessageListRequest{Agent: renamed}); !equalTitles(got, []string{"incoming"}) {
		t.Fatalf("inbox after rename = %v", got)
	}
	if got := inboxTitles(t, sys, app.MessageListRequest{Agent: string(alice.ID), IncludeSelf: true}); len(got) != 2 {
		t.Fatalf("inbox after rename with IncludeSelf = %v", got)
	}
}

func TestInboxSelfExclusionRespectsExpiryAndRouting(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	carol := join(t, sys.service, "carol", nil)
	topic := createTopic(t, sys.service, "build")
	for _, agent := range []domain.Agent{alice, bob, carol} {
		follow(t, sys.service, agent, topic)
	}
	sys.clock.Advance(time.Millisecond)
	fleeting, e := sys.service.Publish(ctx, app.PublishRequest{Author: "bob", Topic: "build", Title: "fleeting", Body: "x", Expiry: app.Expiry{After: time.Minute}})
	if e != nil {
		t.Fatal(e)
	}
	publish(t, sys, "bob", "build", "durable")
	directSend(t, sys, "bob", "carol", "unrelated-direct")

	if got := inboxTitles(t, sys, app.MessageListRequest{Agent: "alice"}); !equalTitles(got, []string{"durable", "fleeting"}) {
		t.Fatalf("alice inbox = %v", got)
	}
	sys.clock.Advance(2 * time.Minute)
	got := inboxTitles(t, sys, app.MessageListRequest{Agent: "alice"})
	if !equalTitles(got, []string{"durable"}) {
		t.Fatalf("alice inbox after expiry = %v", got)
	}
	if got := inboxTitles(t, sys, app.MessageListRequest{Agent: "alice", IncludeSelf: true}); !equalTitles(got, []string{"durable"}) {
		t.Fatalf("IncludeSelf must not resurrect expired messages: %v", got)
	}
	// A direct topic alice does not belong to stays out of her inbox either way.
	for _, includeSelf := range []bool{false, true} {
		for _, title := range inboxTitles(t, sys, app.MessageListRequest{Agent: "alice", IncludeSelf: includeSelf}) {
			if title == "unrelated-direct" {
				t.Fatal("unrelated direct traffic leaked into the inbox")
			}
		}
	}
	_ = fleeting
}

func waitTitles(t *testing.T, result app.MessageWaitResponse) []string {
	t.Helper()
	titles := make([]string, 0, len(result.Items))
	for _, m := range result.Items {
		titles = append(titles, m.Title)
	}
	return titles
}

func TestWaitForMessagesFiltersAndReturnsPreexistingMatchesImmediately(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	carol := join(t, sys.service, "carol", nil)
	topic := createTopic(t, sys.service, "build")
	for _, agent := range []domain.Agent{alice, bob, carol} {
		follow(t, sys.service, agent, topic)
	}
	mine := publish(t, sys, "alice", "build", "alice-root")
	bobReply := replyTo(t, sys, "bob", mine, "bob-reply")
	carolReply := replyTo(t, sys, "carol", mine, "carol-reply")
	carolElsewhere := publish(t, sys, "carol", "build", "carol-root")

	short := 100 * time.Millisecond
	cases := []struct {
		name string
		req  app.MessageWaitRequest
		want []string
	}{
		{"no filter", app.MessageWaitRequest{Agent: "alice"}, []string{"bob-reply", "carol-reply", "carol-root"}},
		{"author only", app.MessageWaitRequest{Agent: "alice", From: "carol"}, []string{"carol-reply", "carol-root"}},
		{"thread only", app.MessageWaitRequest{Agent: "alice", Thread: string(mine.ID)}, []string{"bob-reply", "carol-reply"}},
		{"author and thread", app.MessageWaitRequest{Agent: "alice", From: "carol", Thread: string(mine.ID)}, []string{"carol-reply"}},
		{"thread named by a reply", app.MessageWaitRequest{Agent: "alice", Thread: string(bobReply.ID)}, []string{"bob-reply", "carol-reply"}},
		{"author by stable id", app.MessageWaitRequest{Agent: "alice", From: string(bob.ID)}, []string{"bob-reply"}},
		{"bounded batch", app.MessageWaitRequest{Agent: "alice", Limit: 1}, []string{"bob-reply"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.req.Timeout = short
			result, e := sys.service.WaitForMessages(ctx, testCase.req)
			if e != nil {
				t.Fatal(e)
			}
			if got := waitTitles(t, result); !equalTitles(got, testCase.want) {
				t.Fatalf("wait = %v, want %v", got, testCase.want)
			}
			if result.Filter.AgentID != alice.ID {
				t.Fatalf("resolved filter = %#v", result.Filter)
			}
		})
	}
	// Own messages never satisfy a wait unless asked for.
	if _, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", From: "alice", Timeout: short}); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("waiting on own author = %v, want timeout", e)
	}
	result, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", From: "alice", IncludeSelf: true, Timeout: short})
	if e != nil || !equalTitles(waitTitles(t, result), []string{"alice-root"}) {
		t.Fatalf("IncludeSelf wait = %#v %v", result, e)
	}
	// Traffic that does not match must not satisfy or reset the wait.
	if _, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", From: "bob", Thread: string(carolElsewhere.ID), Timeout: short}); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("non-matching intersection = %v, want timeout", e)
	}
	// Missing filter targets fail fast rather than waiting.
	for name, req := range map[string]app.MessageWaitRequest{
		"missing author": {Agent: "alice", From: "nobody", Timeout: time.Minute},
		"missing thread": {Agent: "alice", Thread: "msg_absent", Timeout: time.Minute},
		"missing agent":  {Agent: "nobody", Timeout: time.Minute},
	} {
		if _, e := sys.service.WaitForMessages(ctx, req); !errors.Is(e, app.ErrNotFound) {
			t.Fatalf("%s = %v, want not found", name, e)
		}
	}
	if _, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Timeout: -time.Second}); !errors.Is(e, domain.ErrInvalid) {
		t.Fatalf("negative timeout = %v", e)
	}
	if _, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Timeout: 2 * app.MaxWaitTimeout}); !errors.Is(e, domain.ErrInvalid) {
		t.Fatalf("unbounded timeout = %v", e)
	}
	_ = carolReply
}

func TestWaitForMessagesResumesFromCursorWithoutAcknowledging(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)

	publish(t, sys, "bob", "build", "one")
	first, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Limit: 1, Timeout: time.Second})
	if e != nil || !equalTitles(waitTitles(t, first), []string{"one"}) {
		t.Fatalf("first wait = %#v %v", first, e)
	}
	if first.After == "" {
		t.Fatal("first wait returned no continuation cursor")
	}

	// A waiter started before the reply resolves on the arrival, exactly once,
	// with no outer polling loop.
	results := make(chan app.MessageWaitResponse, 1)
	failures := make(chan error, 1)
	go func() {
		result, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", After: first.After, Timeout: 5 * time.Second})
		if e != nil {
			failures <- e
			return
		}
		results <- result
	}()
	time.Sleep(20 * time.Millisecond)
	publish(t, sys, "bob", "build", "two")
	select {
	case e := <-failures:
		t.Fatal(e)
	case second := <-results:
		if !equalTitles(waitTitles(t, second), []string{"two"}) {
			t.Fatalf("resumed wait = %v; the cursor must not replay %q", waitTitles(t, second), "one")
		}
		if second.After == first.After {
			t.Fatal("continuation cursor did not advance")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not observe the arrival")
	}

	// Waiting is a read. No cursor moved and no receipt appeared.
	subscriptions, e := sys.service.Subscriptions(ctx, app.SubscriptionListRequest{Agent: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	if subscriptions.Items[0].ReadThroughSequence != 0 || subscriptions.Items[0].ReadThroughAt != nil {
		t.Fatalf("waiting advanced a read cursor: %#v", subscriptions.Items[0])
	}
	if got := inboxTitles(t, sys, app.MessageListRequest{Agent: "alice", UnreadOnly: true}); !equalTitles(got, []string{"two", "one"}) {
		t.Fatalf("unread after waiting = %v", got)
	}
}

func TestWaitForMessagesCannotMissAnArrivalAtTheSubscribeBoundary(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)

	cursor := ""
	for round := 0; round < 25; round++ {
		start := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			<-start
			_, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", After: cursor, Timeout: 5 * time.Second})
			done <- e
		}()
		close(start)
		published := publish(t, sys, "bob", "build", fmt.Sprintf("round-%d", round))
		select {
		case e := <-done:
			if e != nil {
				t.Fatalf("round %d: publish raced the waiter into a %v", round, e)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: waiter missed an arrival committed at the subscribe boundary", round)
		}
		cursor = encodeCursor(strconv.FormatInt(micros(published.CreatedAt), 10), string(published.ID))
	}
}

func TestConcurrentWaitersAllResolveAndReleaseTheirSubscriptions(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, alice, topic)
	follow(t, sys.service, bob, topic)

	const waiters = 8
	var group sync.WaitGroup
	failures := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Timeout: 5 * time.Second}); e != nil {
				failures <- e
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	publish(t, sys, "bob", "build", "broadcast")
	group.Wait()
	close(failures)
	for e := range failures {
		t.Fatalf("concurrent waiter: %v", e)
	}

	// A timed-out wait and a cancelled wait must also release their slot.
	if _, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", From: "alice", Timeout: 50 * time.Millisecond}); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("timed-out wait = %v", e)
	}
	cancellable, cancel := context.WithCancel(ctx)
	cancelled := make(chan error, 1)
	go func() {
		_, e := sys.service.WaitForMessages(cancellable, app.MessageWaitRequest{Agent: "alice", From: "alice", Timeout: time.Minute})
		cancelled <- e
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case e := <-cancelled:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("cancelled wait = %v, want cancellation distinguishable from timeout", e)
		}
		if errors.Is(e, context.DeadlineExceeded) {
			t.Fatal("cancellation reported as a deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled wait did not return promptly")
	}
	if got := sys.service.MessageEvents().(*app.Notifier).Subscribers(); got != 0 {
		t.Fatalf("%d wait subscriptions leaked", got)
	}
}

func TestWaitForAgentResolvesJoinsAndBoundsMissingHandles(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	existing := join(t, sys.service, "alice", nil)

	// An already registered handle returns promptly.
	got, e := sys.service.WaitForAgent(ctx, app.AgentWaitRequest{Agent: "alice", Timeout: time.Minute})
	if e != nil || got.ID != existing.ID {
		t.Fatalf("registered wait = %#v %v", got, e)
	}

	// A waiter started before the join returns the exact stable ID, with no
	// repeated lookups by the caller.
	joined := make(chan domain.Agent, 1)
	failures := make(chan error, 1)
	go func() {
		agent, e := sys.service.WaitForAgent(ctx, app.AgentWaitRequest{Agent: "publisher", Timeout: 5 * time.Second})
		if e != nil {
			failures <- e
			return
		}
		joined <- agent
	}()
	time.Sleep(20 * time.Millisecond)
	fresh := join(t, sys.service, "publisher", nil)
	select {
	case e := <-failures:
		t.Fatal(e)
	case agent := <-joined:
		if agent.ID != fresh.ID {
			t.Fatalf("waiter returned %s, want the joined agent %s", agent.ID, fresh.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not observe the join")
	}

	// A missing handle ends at its bound, and a cancelled wait is distinct.
	start := time.Now()
	if _, e := sys.service.WaitForAgent(ctx, app.AgentWaitRequest{Agent: "absent", Timeout: 100 * time.Millisecond}); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("missing handle = %v, want timeout", e)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("wait overran its bound by %s", elapsed)
	}
	cancellable, cancel := context.WithCancel(ctx)
	cancelled := make(chan error, 1)
	go func() {
		_, e := sys.service.WaitForAgent(cancellable, app.AgentWaitRequest{Agent: "absent", Timeout: time.Minute})
		cancelled <- e
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case e := <-cancelled:
		if !errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("cancelled agent wait = %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled agent wait did not return promptly")
	}

	// Waiting creates nothing and takes nothing over.
	agents, e := sys.service.Agents(ctx, app.AgentListRequest{})
	if e != nil {
		t.Fatal(e)
	}
	if len(agents.Items) != 2 {
		t.Fatalf("waiting mutated the agent roster: %#v", agents.Items)
	}
	if got := sys.service.AgentEvents().(*app.Notifier).Subscribers(); got != 0 {
		t.Fatalf("%d agent wait subscriptions leaked", got)
	}
}

func TestWaitForAgentRefusesRetiredHandlesAndFollowsRenames(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	retiring := join(t, sys.service, "retiring", nil)
	if _, e := sys.service.RetireAgent(ctx, app.RetireAgentRequest{Agent: string(retiring.ID)}); e != nil {
		t.Fatal(e)
	}
	// A retired handle stays taken, so this is a conflict rather than a wait
	// that can only ever expire.
	if _, e := sys.service.WaitForAgent(ctx, app.AgentWaitRequest{Agent: "retiring", Timeout: time.Minute}); !errors.Is(e, app.ErrConflict) {
		t.Fatalf("retired handle = %v, want conflict", e)
	}

	// A rename that makes the awaited handle resolve also wakes a waiter.
	mover := join(t, sys.service, "mover", nil)
	resolved := make(chan domain.Agent, 1)
	failures := make(chan error, 1)
	go func() {
		agent, e := sys.service.WaitForAgent(ctx, app.AgentWaitRequest{Agent: "arrived", Timeout: 5 * time.Second})
		if e != nil {
			failures <- e
			return
		}
		resolved <- agent
	}()
	time.Sleep(20 * time.Millisecond)
	renamed := "arrived"
	if _, e := sys.service.UpdateAgent(ctx, app.UpdateAgentRequest{Agent: string(mover.ID), Handle: &renamed}); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-failures:
		t.Fatal(e)
	case agent := <-resolved:
		if agent.ID != mover.ID {
			t.Fatalf("rename waiter returned %s, want %s", agent.ID, mover.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rename did not wake the waiter")
	}
	if _, e := sys.service.WaitForAgent(ctx, app.AgentWaitRequest{Agent: "  ", Timeout: time.Minute}); !errors.Is(e, domain.ErrInvalid) {
		t.Fatalf("blank reference = %v", e)
	}
}

// Following a topic that already holds unread messages routes them to the
// agent, which satisfies a wait that is already blocked on that predicate.
func TestFollowingATopicWakesABlockedWaiter(t *testing.T) {
	sys := newTestSystem(t)
	ctx := context.Background()
	alice := join(t, sys.service, "alice", nil)
	bob := join(t, sys.service, "bob", nil)
	topic := createTopic(t, sys.service, "build")
	follow(t, sys.service, bob, topic)
	publish(t, sys, "bob", "build", "already-here")

	waited := make(chan app.MessageWaitResponse, 1)
	failures := make(chan error, 1)
	go func() {
		result, e := sys.service.WaitForMessages(ctx, app.MessageWaitRequest{Agent: "alice", Timeout: 5 * time.Second})
		if e != nil {
			failures <- e
			return
		}
		waited <- result
	}()
	time.Sleep(50 * time.Millisecond)
	follow(t, sys.service, alice, topic)
	select {
	case e := <-failures:
		t.Fatalf("waiter slept through a subscription that satisfied it: %v", e)
	case result := <-waited:
		if !equalTitles(waitTitles(t, result), []string{"already-here"}) {
			t.Fatalf("wait after follow = %v", waitTitles(t, result))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("following a topic with unread messages did not wake the waiter")
	}
}
