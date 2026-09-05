package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcus/comms/internal/domain"
)

type agentStoreFake struct {
	joined domain.Agent
	now    time.Time
}

func (f *agentStoreFake) JoinAgent(_ context.Context, _ JoinRequest, a domain.Agent, now time.Time) (JoinResponse, error) {
	f.joined = a
	f.now = now
	return JoinResponse{Agent: a}, nil
}
func (*agentStoreFake) GetAgent(context.Context, string, bool, time.Time) (domain.Agent, error) {
	return domain.Agent{}, ErrNotFound
}
func (*agentStoreFake) UpdateAgent(context.Context, UpdateAgentRequest, time.Time) (domain.Agent, error) {
	return domain.Agent{}, nil
}
func (*agentStoreFake) RetireAgent(context.Context, RetireAgentRequest, time.Time) (domain.Agent, error) {
	return domain.Agent{}, nil
}
func (*agentStoreFake) ListAgents(context.Context, AgentListRequest, time.Time) (Page[domain.Agent], error) {
	return Page[domain.Agent]{}, nil
}

func TestJoinUsesFocusedStoreAndInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 9, 4, 1, 2, 3, 456000, time.FixedZone("local", 3600))
	fake := &agentStoreFake{}
	service := NewServiceWithStores(fake, nil, nil, nil, domain.FixedClock{Time: fixed})
	got, e := service.Join(context.Background(), JoinRequest{})
	if e != nil {
		t.Fatal(e)
	}
	if got.Agent.Handle == "" || fake.joined.ID == "" {
		t.Fatalf("join=%#v", got)
	}
	if !fake.now.Equal(fixed) || fake.now.Location() != time.UTC {
		t.Fatalf("now=%v", fake.now)
	}
}

func TestValidationStopsBeforeStore(t *testing.T) {
	fake := &agentStoreFake{}
	service := NewServiceWithStores(fake, nil, nil, nil, domain.FixedClock{Time: time.Now()})
	_, e := service.Join(context.Background(), JoinRequest{Handle: "invalid handle"})
	if !errors.Is(e, domain.ErrInvalid) {
		t.Fatalf("error=%v", e)
	}
	if fake.joined.ID != "" {
		t.Fatal("invalid request reached store")
	}
	if _, e = service.GetAgent(context.Background(), "", false); !errors.Is(e, domain.ErrInvalid) {
		t.Fatalf("missing ref error=%v", e)
	}
}

func TestExpiryOverrides(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		value   Expiry
		want    *time.Time
		invalid bool
	}{{name: "default", want: ptr(now.Add(domain.DefaultRetention))}, {name: "never", value: Expiry{Never: true}}, {name: "duration", value: Expiry{After: time.Hour}, want: ptr(now.Add(time.Hour))}, {name: "absolute", value: Expiry{At: ptr(now.Add(2 * time.Hour))}, want: ptr(now.Add(2 * time.Hour))}, {name: "conflict", value: Expiry{Never: true, After: time.Hour}, invalid: true}, {name: "past", value: Expiry{At: ptr(now.Add(-time.Second))}, invalid: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, e := tt.value.resolve(now)
			if (e != nil) != tt.invalid {
				t.Fatalf("expiry=%v error=%v", got, e)
			}
			if tt.want == nil && got != nil {
				t.Fatalf("got=%v want nil", got)
			}
			if tt.want != nil && (got == nil || !got.Equal(*tt.want)) {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}
func ptr[T any](v T) *T { return &v }

func TestNotifierWakesEveryCurrentSubscriberAndReleasesSlots(t *testing.T) {
	var notifier Notifier
	notifier.Notify() // No subscribers must not panic or block.

	first, releaseFirst := notifier.Subscribe()
	second, releaseSecond := notifier.Subscribe()
	if notifier.Subscribers() != 2 {
		t.Fatalf("subscribers=%d", notifier.Subscribers())
	}
	notifier.Notify()
	for name, signals := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-signals:
		default:
			t.Fatalf("%s subscriber was not woken", name)
		}
	}

	// Sends never block: repeated notifications coalesce into the one pending
	// wake-up that is enough to force a fresh predicate check.
	for i := 0; i < 100; i++ {
		notifier.Notify()
	}
	if len(first) != 1 {
		t.Fatalf("pending wake-ups=%d, want 1", len(first))
	}

	releaseFirst()
	releaseFirst() // Releasing twice is safe.
	if notifier.Subscribers() != 1 {
		t.Fatalf("subscribers after release=%d", notifier.Subscribers())
	}
	notifier.Notify()
	select {
	case <-second:
	default:
		t.Fatal("remaining subscriber was not woken")
	}
	releaseSecond()
	if notifier.Subscribers() != 0 {
		t.Fatalf("subscribers after all releases=%d", notifier.Subscribers())
	}
}
