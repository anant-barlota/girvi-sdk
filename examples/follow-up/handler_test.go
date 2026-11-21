package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anant-barlota/girvi-sdk/plugin"
	"github.com/anant-barlota/girvi-sdk/plugin/plugintest"
)

var settings = map[string]string{"url": "https://hooks.example.com/in", "token": "t0ken", "reminder_days": "30", "share_phone": "false"}

func with(extra map[string]string) map[string]string {
	s := map[string]string{}
	for k, v := range settings {
		s[k] = v
	}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

func sent(t *testing.T, r plugin.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func init() { now = func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) } }

// A new loan is posted, remembered and given a reminder; a retry of the same
// event changes nothing more.
func TestLoanCreated(t *testing.T) {
	h := plugintest.New()
	in := plugintest.Event(t, plugin.EventLoanCreated, settings)
	if out := handle(h, in); out.Kind != "ok" || out.Note != "Sent (HTTP 200). Open loans: 1." {
		t.Fatalf("outcome %+v", out)
	}
	r := h.Requests[0]
	m := sent(t, r)
	if r.Headers["Authorization"] != "Bearer t0ken" || r.Headers["Idempotency-Key"] != in.DeliveryID || m["loan"] != "F-1024" || m["customer"] != "Asha Patel" {
		t.Fatalf("request %+v %v", r.Headers, m)
	}
	if _, shared := m["phone"]; shared {
		t.Error("the phone was sent without share_phone")
	}
	if len(h.Wakes) != 1 || h.Wakes[0].At != now().Add(30*24*time.Hour).Unix() || string(h.Storage[openKey]) != "1" {
		t.Fatalf("wakes %+v storage %v", h.Wakes, h.Keys())
	}

	in.Attempt = 2
	handle(h, in)
	if len(h.Wakes) != 1 || string(h.Storage[openKey]) != "1" {
		t.Errorf("a retry counted or scheduled again: wakes %+v open %s", h.Wakes, h.Storage[openKey])
	}
}

func TestPhoneOnlyWhenShared(t *testing.T) {
	h := plugintest.New()
	handle(h, plugintest.Event(t, plugin.EventPaymentReceived, with(map[string]string{"share_phone": "true"})))
	if m := sent(t, h.Requests[0]); m["phone"] != "+919876543210" || m["amount"] != "₹10,350.00" {
		t.Errorf("sent %v", m)
	}
}

// Send test is posted, but its made-up loan is not remembered.
func TestSendTest(t *testing.T) {
	h := plugintest.New()
	in := plugintest.Event(t, plugin.EventLoanCreated, settings)
	in.Kind = plugin.KindTest
	if out := handle(h, in); out.Kind != "ok" || len(h.Requests) != 1 || len(h.Wakes) != 0 || len(h.Storage) != 0 {
		t.Fatalf("outcome %+v wakes %v storage %v", out, h.Wakes, h.Keys())
	}
}

// The reminder is sent for a loan still open, with a phone notification if
// asked, and not for one redeemed in the meantime.
func TestReminders(t *testing.T) {
	h := plugintest.New()
	handle(h, plugintest.Event(t, plugin.EventLoanCreated, settings))
	loanID := h.Wakes[0].Payload

	wake := plugintest.WakeInput(loanID, with(map[string]string{"ntfy_topic": "bk-moolchand"}))
	if out := handle(h, wake); out.Kind != "ok" {
		t.Fatalf("reminder %+v", out)
	}
	ntfy, reminder := h.Requests[1], h.Requests[2]
	if ntfy.URL != "https://ntfy.sh/bk-moolchand" || string(ntfy.Body) != "Loan F-1024 is still open after 30 days." {
		t.Errorf("ntfy %+v %s", ntfy, ntfy.Body)
	}
	if m := sent(t, reminder); m["event"] != "reminder" || m["loan"] != "F-1024" {
		t.Errorf("reminder %v", m)
	}

	handle(h, plugintest.Event(t, plugin.EventLoanRedeemed, settings))
	if string(h.Storage[openKey]) != "0" {
		t.Errorf("open after redemption: %s", h.Storage[openKey])
	}
	before := len(h.Requests)
	if out := handle(h, wake); out.Kind != "ok" || len(h.Requests) != before {
		t.Errorf("a reminder for a redeemed loan: %+v, %d requests", out, len(h.Requests)-before)
	}
}

// The plugin decides what each answer means, and backs off on retries.
func TestOutcomes(t *testing.T) {
	for _, c := range []struct {
		answer  plugin.Response
		attempt int
		kind    string
		retryIn int
	}{
		{plugin.Response{Status: 204}, 1, "ok", 0},
		{plugin.Response{Status: 401}, 1, "blocked", 0},
		{plugin.Response{Status: 503}, 1, "retry", 60},
		{plugin.Response{Status: 429}, 3, "retry", 240},
		{plugin.Response{Error: "timed out"}, 9, "retry", 3600},
		{plugin.Response{Status: 422}, 1, "failed", 0},
	} {
		h := plugintest.New()
		h.Respond(c.answer)
		in := plugintest.Event(t, plugin.EventPaymentReceived, settings)
		in.Attempt = c.attempt
		if out := handle(h, in); out.Kind != c.kind || out.RetryInSeconds != c.retryIn {
			t.Errorf("%+v on attempt %d: %+v", c.answer, c.attempt, out)
		}
	}
}

func TestSettingsProblemsBlock(t *testing.T) {
	for name, s := range map[string]map[string]string{
		"no URL":   with(map[string]string{"url": ""}),
		"bad days": with(map[string]string{"reminder_days": "soon"}),
		"too many": with(map[string]string{"reminder_days": "400"}),
	} {
		out := handle(plugintest.New(), plugintest.Event(t, plugin.EventLoanCreated, s))
		if out.Kind != "blocked" || strings.Contains(out.Note, "t0ken") {
			t.Errorf("%s: %+v", name, out)
		}
	}
}
