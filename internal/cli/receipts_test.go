package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

func at(now time.Time, ago time.Duration) *time.Time {
	v := now.Add(-ago)
	return &v
}

func TestRenderReceiptsShowsFourStatesAndInspectors(t *testing.T) {
	now := time.Date(2026, 9, 8, 19, 34, 0, 0, time.UTC)
	report := app.ReceiptReport{
		Subscribers: []app.Receipt{
			{Agent: domain.Agent{Handle: "claude-reviewer", Harness: "claude"}, State: "read", ReadAt: at(now, 2*time.Minute), Retrieval: app.Retrieval{SeenAt: at(now, 4*time.Minute), InspectedAt: at(now, 3*time.Minute), SeenCount: 2}},
			{Agent: domain.Agent{Handle: "codex-worker"}, State: "unread", Retrieval: app.Retrieval{SeenAt: at(now, 4*time.Minute), InspectedAt: at(now, 4*time.Minute), SeenCount: 3}},
			{Agent: domain.Agent{Handle: "test-agent"}, State: "unread", Retrieval: app.Retrieval{SeenAt: at(now, 5*time.Minute), SeenCount: 1}},
			{Agent: domain.Agent{Handle: "backup-worker"}, State: "unread"},
		},
		Inspectors: []app.Inspector{
			{Agent: domain.Agent{Handle: "orchestrator", Harness: "antigravity"}, Retrieval: app.Retrieval{SeenAt: at(now, time.Minute), InspectedAt: at(now, time.Minute), SeenCount: 1}},
		},
	}
	var out bytes.Buffer
	if err := renderReceipts(&out, report, false, now); err != nil {
		t.Fatal(err)
	}
	want := "Subscribers:\n" +
		"  @claude-reviewer  read 2m ago\n" +
		"  @codex-worker     inspected full body 4m ago, not acknowledged\n" +
		"  @test-agent       saw preview 5m ago, not acknowledged\n" +
		"  @backup-worker    unseen\n" +
		"Inspectors (not subscribed):\n" +
		"  @orchestrator     inspected full body 1m ago\n"
	if out.String() != want {
		t.Fatalf("receipts output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestRenderReceiptsDetailedAddsAbsoluteFactsAndEmptyCases(t *testing.T) {
	now := time.Date(2026, 9, 8, 19, 34, 0, 0, time.UTC)
	report := app.ReceiptReport{Subscribers: []app.Receipt{
		{Agent: domain.Agent{Handle: "codex-worker", Harness: "codex", Project: "comms"}, State: "unread", Retrieval: app.Retrieval{SeenAt: at(now, 4*time.Minute), InspectedAt: at(now, 3*time.Minute), SeenCount: 3}},
		{Agent: domain.Agent{Handle: "backup-worker"}, State: "unread"},
	}}
	var out bytes.Buffer
	if err := renderReceipts(&out, report, true, now); err != nil {
		t.Fatal(err)
	}
	want := "Subscribers:\n" +
		"  @codex-worker   inspected full body 3m ago, not acknowledged\n" +
		"    seen=2026-09-08T19:30:00Z inspected=2026-09-08T19:31:00Z visits=3 codex/comms\n" +
		"  @backup-worker  unseen\n" +
		"    no activity recorded\n"
	if out.String() != want {
		t.Fatalf("detailed output:\n%s\nwant:\n%s", out.String(), want)
	}

	var empty bytes.Buffer
	if err := renderReceipts(&empty, app.ReceiptReport{}, false, now); err != nil {
		t.Fatal(err)
	}
	if empty.String() != "Subscribers:\n  none\n" {
		t.Fatalf("empty report output=%q", empty.String())
	}
}

func TestRelativeTimeScalesUnits(t *testing.T) {
	now := time.Date(2026, 9, 8, 19, 34, 0, 0, time.UTC)
	tests := []struct {
		ago  time.Duration
		want string
	}{
		{0, "0s ago"},
		{45 * time.Second, "45s ago"},
		{90 * time.Second, "1m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
		{-time.Minute, "0s ago"},
	}
	for _, tt := range tests {
		if got := relativeTime(at(now, tt.ago), now); got != tt.want {
			t.Errorf("relativeTime(%s)=%q want %q", tt.ago, got, tt.want)
		}
	}
	if got := relativeTime(nil, now); got != "at an unknown time" {
		t.Errorf("relativeTime(nil)=%q", got)
	}
}
