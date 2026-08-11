package messages

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMessageItemDeliveryStateDefaultsToSent(t *testing.T) {
	item := MessageItem{}
	if item.DeliveryState != DeliverySent {
		t.Fatalf("DeliveryState=%v want DeliverySent", item.DeliveryState)
	}
}

func TestReplaceLocalMessageFindsCorrelationOrLocalID(t *testing.T) {
	for _, identity := range []string{"corr-1", "local-post-1"} {
		t.Run(identity, func(t *testing.T) {
			m := New([]MessageItem{{
				ID:            "local-post-1",
				TS:            "must-not-identify-mattermost-local",
				CorrelationID: "corr-1",
				DeliveryState: DeliveryPending,
				Text:          "optimistic",
			}}, "general")
			authoritative := MessageItem{ID: "server-post-1", Text: "authoritative"}

			if !m.ReplaceLocalMessage(identity, authoritative) {
				t.Fatal("ReplaceLocalMessage returned false")
			}
			if got := m.Messages(); len(got) != 1 || !reflect.DeepEqual(got[0], authoritative) {
				t.Fatalf("messages=%#v want authoritative replacement", got)
			}
		})
	}
}

func TestReplaceLocalMessageCollapsesAuthoritativeRowThatArrivedFirst(t *testing.T) {
	m := New([]MessageItem{
		{ID: "local:opaque", CorrelationID: "corr/opaque", DeliveryState: DeliveryPending, Text: "pending"},
		{ID: "post/opaque", CorrelationID: "corr/opaque", Format: FormatMattermostPlain, Text: "history"},
	}, "general")

	response := MessageItem{ID: "post/opaque", CorrelationID: "corr/opaque", Format: FormatMattermostPlain, Text: "post response"}
	if !m.ReplaceLocalMessage("corr/opaque", response) {
		t.Fatal("ReplaceLocalMessage returned false")
	}
	if got := m.Messages(); len(got) != 1 || !reflect.DeepEqual(got[0], response) {
		t.Fatalf("messages=%#v want one response row", got)
	}
}

func TestMattermostCorrelationReconciliationHandlesBothAuthoritativeEventOrders(t *testing.T) {
	local := MessageItem{ID: "local:opaque", CorrelationID: "corr/opaque", DeliveryState: DeliveryPending, Format: FormatMattermostPlain, Text: "pending"}
	history := MessageItem{ID: "post/opaque", CorrelationID: "corr/opaque", Format: FormatMattermostPlain, Text: "history"}
	response := MessageItem{ID: "post/opaque", CorrelationID: "corr/opaque", Format: FormatMattermostPlain, Text: "post response"}

	t.Run("history then post response", func(t *testing.T) {
		m := New([]MessageItem{local}, "general")
		m.ReconcileRecentPage([]string{"local:opaque"}, []string{"post/opaque"}, nil, []MessageItem{history}, false)
		if !m.ReplaceLocalMessage("corr/opaque", response) {
			t.Fatal("POST response did not find reconciled correlation")
		}
		if got := m.Messages(); len(got) != 1 || !reflect.DeepEqual(got[0], response) {
			t.Fatalf("messages=%#v want one POST response row", got)
		}
	})

	t.Run("post response then history", func(t *testing.T) {
		m := New([]MessageItem{local}, "general")
		if !m.ReplaceLocalMessage("corr/opaque", response) {
			t.Fatal("POST response did not replace local row")
		}
		m.ReconcileRecentPage([]string{"local:opaque"}, []string{"post/opaque"}, nil, []MessageItem{history}, false)
		if got := m.Messages(); len(got) != 1 || !reflect.DeepEqual(got[0], history) {
			t.Fatalf("messages=%#v want one history row", got)
		}
	})
}

func TestMattermostPriorSessionAuthoritativeCorrelationDoesNotCollapseNewPendingRow(t *testing.T) {
	old := MessageItem{ID: "old/post", CorrelationID: "mmk-prior-session", Format: FormatMattermostPlain, Text: "old authoritative"}
	pending := MessageItem{ID: "mmk-new-session", CorrelationID: "mmk-new-session", DeliveryState: DeliveryPending, Format: FormatMattermostPlain, Text: "new pending"}
	m := New([]MessageItem{pending}, "general")

	m.ReconcileRecentPage(nil, []string{"old/post"}, nil, []MessageItem{old}, false)

	if got := m.Messages(); len(got) != 2 || !reflect.DeepEqual(got[0], old) || !reflect.DeepEqual(got[1], pending) {
		t.Fatalf("messages=%#v want old authoritative and distinct new pending", got)
	}
}

func TestMattermostAuthoritativePostWithoutCorrelationStillDedupesByPostID(t *testing.T) {
	m := New([]MessageItem{
		{ID: "local:opaque", CorrelationID: "corr/opaque", DeliveryState: DeliveryPending, Format: FormatMattermostPlain},
		{ID: "post/opaque", Format: FormatMattermostPlain, Text: "history without pending id"},
	}, "general")
	response := MessageItem{ID: "post/opaque", Format: FormatMattermostPlain, Text: "response without pending id"}
	if !m.ReplaceLocalMessage("corr/opaque", response) {
		t.Fatal("POST response did not find local correlation")
	}
	if got := m.Messages(); len(got) != 1 || got[0].ID != "post/opaque" || got[0].Text != response.Text {
		t.Fatalf("messages=%#v want one opaque post", got)
	}
}

func TestMattermostOlderReconciliationCollapsesTransientByCorrelation(t *testing.T) {
	local := MessageItem{ID: "local:older", CorrelationID: "corr/older", DeliveryState: DeliveryFailed, Format: FormatMattermostPlain, Text: "failed"}
	anchor := MessageItem{ID: "anchor/opaque", Format: FormatMattermostPlain}
	m := New([]MessageItem{local, anchor}, "general")
	authoritative := MessageItem{ID: "post/older:opaque", CorrelationID: "corr/older", Format: FormatMattermostPlain, Text: "authoritative older"}

	m.ReconcileOlderPage("anchor/opaque", nil, []string{"post/older:opaque"}, nil, []MessageItem{authoritative}, true)

	if got := m.Messages(); len(got) != 2 || !reflect.DeepEqual(got[0], authoritative) || got[1].ID != "anchor/opaque" {
		t.Fatalf("messages=%#v want authoritative then anchor", got)
	}
}

func TestMarkFailedAndPendingForRetryKeepTransientRow(t *testing.T) {
	m := New([]MessageItem{{
		ID:            "local-post-1",
		CorrelationID: "corr-1",
		DeliveryState: DeliveryPending,
		Text:          "keep me",
	}}, "general")

	if !m.MarkMessageFailed("corr-1", "request rejected") {
		t.Fatal("MarkMessageFailed returned false")
	}
	failed := m.Messages()[0]
	if failed.DeliveryState != DeliveryFailed || failed.FailureReason != "request rejected" || failed.Text != "keep me" {
		t.Fatalf("failed item=%#v", failed)
	}

	if !m.MarkMessagePending("corr-1") {
		t.Fatal("MarkMessagePending returned false")
	}
	pending := m.Messages()[0]
	if pending.DeliveryState != DeliveryPending || pending.FailureReason != "" || pending.Text != "keep me" {
		t.Fatalf("pending item=%#v", pending)
	}
}

func TestMarkMessageFailedDoesNotDowngradeAuthoritativeSentRow(t *testing.T) {
	authoritative := MessageItem{
		ID:            "opaque/post:id",
		CorrelationID: "corr/opaque",
		DeliveryState: DeliverySent,
		Format:        FormatMattermostPlain,
		Text:          "authoritative history",
	}
	m := New([]MessageItem{authoritative}, "general")

	if m.MarkMessageFailed("corr/opaque", "late POST failure") {
		t.Fatal("MarkMessageFailed reported an update for a sent row")
	}
	if got := m.Messages(); len(got) != 1 || !reflect.DeepEqual(got[0], authoritative) {
		t.Fatalf("messages=%#v want unchanged authoritative row", got)
	}
}

func TestMarkMessageFailedSanitizesStoredReason(t *testing.T) {
	m := New([]MessageItem{{CorrelationID: "corr-1", DeliveryState: DeliveryPending}}, "general")
	reason := "  rejected\x1b[31m\n\t" + strings.Repeat("x", 200)

	if !m.MarkMessageFailed("corr-1", reason) {
		t.Fatal("MarkMessageFailed returned false")
	}
	got := m.Messages()[0].FailureReason
	if strings.ContainsAny(got, "\x1b\n\t") {
		t.Fatalf("control characters survived: %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("whitespace was not normalized: %q", got)
	}
	if len([]rune(got)) > 160 {
		t.Fatalf("reason length=%d want <=160", len([]rune(got)))
	}
}

func TestFindAndUpdateMessageByCorrelationID(t *testing.T) {
	m := New([]MessageItem{{ID: "local-post-1", CorrelationID: "corr-1", Text: "before"}}, "general")

	item, ok := m.FindMessageByCorrelationID("corr-1")
	if !ok || item.Text != "before" {
		t.Fatalf("FindMessageByCorrelationID=(%#v, %v)", item, ok)
	}
	if !m.UpdateMessageByCorrelationID("corr-1", func(item *MessageItem) {
		item.Text = "after"
	}) {
		t.Fatal("UpdateMessageByCorrelationID returned false")
	}
	if got := m.Messages()[0].Text; got != "after" {
		t.Fatalf("Text=%q want after", got)
	}
	if _, ok := m.FindMessageByCorrelationID(""); ok {
		t.Fatal("empty correlation ID should not match")
	}
}

func TestMattermostRowsRenderTransientDeliveryIndicatorsWithoutFailureDetails(t *testing.T) {
	const secret = "token=super-secret-value"
	m := New([]MessageItem{
		{ID: "pending", Format: FormatMattermostPlain, Text: "sending", DeliveryState: DeliveryPending},
		{ID: "failed", Format: FormatMattermostPlain, Text: "retry me", DeliveryState: DeliveryFailed, FailureReason: secret},
		{TS: "slack", Format: FormatSlack, Text: "slack pending", DeliveryState: DeliveryPending},
	}, "general")

	out := ansi.Strip(m.View(30, 80))
	if !strings.Contains(out, "[pending]") {
		t.Fatalf("pending indicator missing from %q", out)
	}
	if !strings.Contains(out, "[failed: press enter to retry]") {
		t.Fatalf("failed retry indicator missing from %q", out)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "super-secret-value") {
		t.Fatalf("failure detail leaked into rendered output: %q", out)
	}
	if strings.Count(out, "[pending]") != 1 {
		t.Fatalf("Slack row should not render delivery state: %q", out)
	}
}

func TestMattermostReconciliationPreservesTransientRowsOutsideAuthoritativeIDs(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Model)
		want []string
	}{
		{
			name: "terminal recent",
			run: func(m *Model) {
				m.ReconcileRecentPage([]string{"cached"}, []string{"live"}, nil, []MessageItem{{ID: "live"}}, false)
			},
			want: []string{"live", "local-pending", "local-failed"},
		},
		{
			name: "terminal older",
			run: func(m *Model) {
				m.ReconcileOlderPage("anchor", []string{"cached"}, []string{"oldest"}, nil, []MessageItem{{ID: "oldest"}}, false)
			},
			want: []string{"local-pending", "oldest", "anchor", "local-failed", "new"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New([]MessageItem{
				{ID: "stale"},
				{ID: "local-pending", CorrelationID: "corr-p", DeliveryState: DeliveryPending},
				{ID: "cached"},
				{ID: "anchor"},
				{ID: "local-failed", CorrelationID: "corr-f", DeliveryState: DeliveryFailed},
				{ID: "new"},
			}, "general")

			tc.run(&m)
			if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ids=%v want %v", got, tc.want)
			}
		})
	}
}

func TestNonterminalReconciliationPreservesCapturedTransientRow(t *testing.T) {
	m := New([]MessageItem{
		{ID: "older"},
		{ID: "cached"},
		{ID: "local-pending", CorrelationID: "corr-1", DeliveryState: DeliveryPending},
	}, "general")

	m.ReconcileRecentPage(
		[]string{"cached", "local-pending"},
		[]string{"live"},
		nil,
		[]MessageItem{{ID: "live"}},
		true,
	)

	if got, want := messageIDs(m.Messages()), []string{"older", "live", "local-pending"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
}

func TestSlackLocalSentOperationsRemainTSBased(t *testing.T) {
	m := New([]MessageItem{{ID: "unrelated-id", TS: "local:1", Text: "optimistic"}}, "general")
	authoritative := MessageItem{TS: "1700000000.000001", Text: "sent"}
	if !m.SwapLocalSent("local:1", authoritative) {
		t.Fatal("SwapLocalSent no longer matched Slack local TS")
	}
	if got := m.Messages()[0]; !reflect.DeepEqual(got, authoritative) {
		t.Fatalf("swapped=%#v want %#v", got, authoritative)
	}

	m = New([]MessageItem{{ID: "unrelated-id", TS: "local:2"}}, "general")
	if !m.RemoveLocalSent("local:2") || len(m.Messages()) != 0 {
		t.Fatalf("RemoveLocalSent no longer removed by Slack local TS: %#v", m.Messages())
	}
}
