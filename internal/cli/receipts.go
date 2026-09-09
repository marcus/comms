package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marcus/comms/internal/app"
)

// receipts renders the two independent facts a receipt carries: whether an
// agent acknowledged the message by advancing its cursor, and how much of the
// message actually reached it. The generic renderer cannot express that, so
// this command owns its own presentation.
func (r *runner) receipts(args []string) error {
	if len(args) == 0 {
		return usage("receipts requires MESSAGE_ID")
	}
	fs := newFlagSet("receipts")
	detailed := fs.Bool("detailed", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() != 0 {
		return usage("unexpected receipts arguments")
	}
	client, err := r.client(false)
	if err != nil {
		return err
	}
	var report app.ReceiptReport
	if err = r.do(client, http.MethodGet, "/v1/messages/"+url.PathEscape(args[0])+"/receipts", nil, nil, &report); err != nil {
		return err
	}
	if r.g.json {
		return r.output(report)
	}
	return renderReceipts(r.env.Stdout, report, *detailed, time.Now().UTC())
}

// receiptState collapses the two dimensions into the one word an operator
// reads first. It matches the derivation Comms Web uses, so the surfaces agree.
func receiptState(state string, retrieval app.Retrieval) string {
	switch {
	case state == "read":
		return "read"
	case retrieval.InspectedAt != nil:
		return "inspected"
	case retrieval.SeenAt != nil:
		return "seen"
	default:
		return "unseen"
	}
}

func renderReceipts(w io.Writer, report app.ReceiptReport, detailed bool, now time.Time) error {
	width := 0
	for _, receipt := range report.Subscribers {
		width = max(width, len(receipt.Agent.Handle))
	}
	for _, inspector := range report.Inspectors {
		width = max(width, len(inspector.Agent.Handle))
	}
	if _, err := fmt.Fprintln(w, "Subscribers:"); err != nil {
		return err
	}
	if len(report.Subscribers) == 0 {
		if _, err := fmt.Fprintln(w, "  none"); err != nil {
			return err
		}
	}
	for _, receipt := range report.Subscribers {
		line := subscriberSummary(receipt, now)
		if err := writeReceiptLine(w, receipt.Agent.Handle, width, line, detailed, receipt.Agent.Harness, receipt.Agent.Project, receipt.ReadAt, receipt.Retrieval); err != nil {
			return err
		}
	}
	if len(report.Inspectors) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "Inspectors (not subscribed):"); err != nil {
		return err
	}
	for _, inspector := range report.Inspectors {
		line := retrievalSummary(inspector.Retrieval, now)
		if err := writeReceiptLine(w, inspector.Agent.Handle, width, line, detailed, inspector.Agent.Harness, inspector.Agent.Project, nil, inspector.Retrieval); err != nil {
			return err
		}
	}
	return nil
}

func subscriberSummary(receipt app.Receipt, now time.Time) string {
	if receiptState(receipt.State, receipt.Retrieval) == "read" {
		return "read " + relativeTime(receipt.ReadAt, now)
	}
	summary := retrievalSummary(receipt.Retrieval, now)
	if summary == "unseen" {
		return summary
	}
	return summary + ", not acknowledged"
}

func retrievalSummary(retrieval app.Retrieval, now time.Time) string {
	switch {
	case retrieval.InspectedAt != nil:
		return "inspected full body " + relativeTime(retrieval.InspectedAt, now)
	case retrieval.SeenAt != nil:
		return "saw preview " + relativeTime(retrieval.SeenAt, now)
	default:
		return "unseen"
	}
}

func writeReceiptLine(w io.Writer, handle string, width int, summary string, detailed bool, harness, project string, readAt *time.Time, retrieval app.Retrieval) error {
	if _, err := fmt.Fprintf(w, "  @%-*s  %s\n", width, handle, summary); err != nil {
		return err
	}
	if !detailed {
		return nil
	}
	facts := []string{}
	if readAt != nil {
		facts = append(facts, "acknowledged="+readAt.UTC().Format(time.RFC3339))
	}
	if retrieval.SeenAt != nil {
		facts = append(facts, "seen="+retrieval.SeenAt.UTC().Format(time.RFC3339))
	}
	if retrieval.InspectedAt != nil {
		facts = append(facts, "inspected="+retrieval.InspectedAt.UTC().Format(time.RFC3339))
	}
	if retrieval.SeenCount != 0 {
		facts = append(facts, fmt.Sprintf("visits=%d", retrieval.SeenCount))
	}
	if context := agentContext(harness, project); context != "" {
		facts = append(facts, context)
	}
	if len(facts) == 0 {
		facts = append(facts, "no activity recorded")
	}
	_, err := fmt.Fprintf(w, "    %s\n", strings.Join(facts, " "))
	return err
}

func agentContext(harness, project string) string {
	switch {
	case harness != "" && project != "":
		return harness + "/" + project
	case harness != "":
		return harness
	case project != "":
		return project
	default:
		return ""
	}
}

// relativeTime renders an instant the way an operator scans it: how long ago.
func relativeTime(at *time.Time, now time.Time) string {
	if at == nil {
		return "at an unknown time"
	}
	elapsed := now.Sub(*at)
	if elapsed < 0 {
		elapsed = 0
	}
	units := []struct {
		limit time.Duration
		size  time.Duration
		label string
	}{
		{time.Minute, time.Second, "s"},
		{time.Hour, time.Minute, "m"},
		{24 * time.Hour, time.Hour, "h"},
	}
	for _, unit := range units {
		if elapsed < unit.limit {
			return fmt.Sprintf("%d%s ago", int(elapsed/unit.size), unit.label)
		}
	}
	return fmt.Sprintf("%dd ago", int(elapsed/(24*time.Hour)))
}
