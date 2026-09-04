package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
