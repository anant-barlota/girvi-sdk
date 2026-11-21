// Follow-up is the SDK's example of everything a plugin can do. It posts each
// loan event to the shop's own service, remembers which loans are open, and
// sends a reminder for a loan still open after the days the shop chose.
//
//   - Events: all three, and Send test.
//   - Customer details: name and phone; the phone is sent only if the shop
//     turns it on.
//   - Network: the host of the URL the shop enters, and ntfy.sh.
//   - Settings: required, secret, with a default, on/off, and advanced.
//   - Storage: which loans are open, and how many.
//   - Wake-ups: the reminders.
//   - Logs, every kind of outcome, and the attempt number for backing off.
//
// Nothing here knows about WebAssembly, so it is tested with plain go test.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anant-barlota/girvi-sdk/plugin"
)

const openKey = "open" // how many loans are open

func loanKey(id string) string { return "loan:" + id }

var now = time.Now

// handle is called for every event, Send test and reminder.
func handle(h plugin.Host, in plugin.Input) plugin.Outcome {
	url := strings.TrimSpace(in.Settings["url"])
	if url == "" {
		return plugin.Blocked("Enter your service URL in the settings.")
	}
	days, err := strconv.Atoi(strings.TrimSpace(in.Settings["reminder_days"]))
	if err != nil || days < 1 || days > 365 {
		return plugin.Blocked("Remind after must be a number of days from 1 to 365.")
	}
	if in.Kind == plugin.KindWake {
		return remind(h, in, url, days)
	}

	msg := map[string]any{"event": in.EventType(), "delivery_id": in.DeliveryID, "firm": in.Firm.Name}
	switch in.EventType() {
	case plugin.EventLoanCreated:
		e, err := in.LoanCreated()
		if err != nil {
			return plugin.Failed(err.Error())
		}
		msg["loan"], msg["principal"] = e.Loan.Number, e.Loan.Principal.Display
		addCustomer(msg, e.Customer, in.Settings)
		// Send test uses a made-up loan: post it, but do not remember it.
		if in.Kind == plugin.KindEvent {
			open(h, e.Loan, days)
		}
	case plugin.EventPaymentReceived:
		e, err := in.PaymentReceived()
		if err != nil {
			return plugin.Failed(err.Error())
		}
		msg["loan"], msg["payment"], msg["amount"] = e.Loan.Number, e.Payment.Number, e.Amount.Display
		msg["outstanding"] = e.OutstandingPrincipal.Display
		addCustomer(msg, e.Customer, in.Settings)
	case plugin.EventLoanRedeemed:
		e, err := in.LoanRedeemed()
		if err != nil {
			return plugin.Failed(err.Error())
		}
		msg["loan"], msg["redeemed_on"] = e.Loan.Number, e.RedeemedOn.ISO
		addCustomer(msg, e.Customer, in.Settings)
		closeLoan(h, e.Loan.ID)
	default:
		return plugin.Failed("Follow-up does not handle " + in.EventType())
	}
	msg["open_loans"] = count(h)
	return post(h, in, url, msg)
}

// open remembers a loan and schedules its reminder. A retried event finds the
// loan already remembered, so it is neither counted nor scheduled twice.
func open(h plugin.Host, loan plugin.Loan, days int) {
	if _, seen := h.StorageGet(loanKey(loan.ID)); seen {
		return
	}
	if !h.StorageSet(loanKey(loan.ID), []byte(loan.Number)) {
		h.Log("could not remember loan " + loan.Number + ": storage is full")
		return
	}
	h.StorageSet(openKey, []byte(strconv.Itoa(count(h)+1)))
	at := now().Add(time.Duration(days) * 24 * time.Hour)
	if !h.WakeAt(at.Unix(), loan.ID) {
		h.Log("could not schedule a reminder for " + loan.Number)
		return
	}
	h.Log(fmt.Sprintf("reminder for %s on %s", loan.Number, at.Format("2 Jan 2006")))
}

// closeLoan forgets a redeemed loan, so its reminder finds nothing to do.
func closeLoan(h plugin.Host, id string) {
	if _, seen := h.StorageGet(loanKey(id)); !seen {
		return
	}
	h.StorageDelete(loanKey(id))
	h.StorageSet(openKey, []byte(strconv.Itoa(max(count(h)-1, 0))))
}

func count(h plugin.Host) int {
	v, _ := h.StorageGet(openKey)
	n, _ := strconv.Atoi(string(v))
	return n
}

// remind is a scheduled call: the payload is the loan's id.
func remind(h plugin.Host, in plugin.Input, url string, days int) plugin.Outcome {
	number, open := h.StorageGet(loanKey(in.Wake))
	if !open {
		return plugin.OK("The loan was redeemed. No reminder needed.")
	}
	text := fmt.Sprintf("Loan %s is still open after %d days.", number, days)
	if topic := strings.TrimSpace(in.Settings["ntfy_topic"]); topic != "" {
		// A notification on the shop's phone is a bonus: if it fails, the
		// reminder to their service still counts.
		r := h.HTTP(plugin.Request{Method: "POST", URL: "https://ntfy.sh/" + topic, Body: []byte(text)})
		if r.Error != "" || r.Status >= 300 {
			h.Log(fmt.Sprintf("ntfy.sh did not take the notification: %s%d", r.Error, r.Status))
		}
	}
	return post(h, in, url, map[string]any{"event": "reminder", "loan": string(number), "days": days, "text": text, "firm": in.Firm.Name, "open_loans": count(h)})
}

func addCustomer(msg map[string]any, c plugin.Customer, settings map[string]string) {
	msg["customer"] = c.Name
	if settings["share_phone"] == "true" {
		msg["phone"] = c.PhoneE164
	}
}

// post sends msg and decides what the answer means.
func post(h plugin.Host, in plugin.Input, url string, msg map[string]any) plugin.Outcome {
	body, _ := json.Marshal(msg)
	headers := map[string]string{
		"Content-Type": "application/json",
		// The same on every retry, so the service can drop a duplicate.
		"Idempotency-Key": in.DeliveryID,
	}
	if token := in.Settings["token"]; token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	r := h.HTTP(plugin.Request{Method: "POST", URL: url, Headers: headers, Body: body})
	switch {
	case r.Error != "":
		return plugin.Retry(backoff(in.Attempt), "Your service did not answer: "+r.Error)
	case r.Status < 300:
		return plugin.OK(fmt.Sprintf("Sent (HTTP %d). Open loans: %d.", r.Status, msg["open_loans"]))
	case r.Status == 401, r.Status == 403:
		return plugin.Blocked(fmt.Sprintf("Your service refused the token (HTTP %d). Check it in the settings.", r.Status))
	case r.Status == 408, r.Status == 429, r.Status >= 500:
		return plugin.Retry(backoff(in.Attempt), fmt.Sprintf("Your service is busy (HTTP %d).", r.Status))
	}
	return plugin.Failed(fmt.Sprintf("Your service refused this (HTTP %d).", r.Status))
}

// backoff waits a minute after the first try, then doubles, up to an hour.
func backoff(attempt int) int {
	return min(60<<min(max(attempt-1, 0), 6), 3600)
}
