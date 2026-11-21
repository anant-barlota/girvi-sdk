# Follow-up (`example/follow-up`)

The SDK's example of everything a plugin can do. It posts each loan event to the shop's own service, remembers
which loans are open, and sends a reminder for a loan still open after the days the shop chose. It is an example:
it is tested with every change to the SDK, but not released or listed in the catalog.

| Capability | Where |
|---|---|
| All three events, and Send test | `handle` in [`handler.go`](handler.go); a test's made-up loan is posted but not remembered |
| Scheduled calls (`on_wake`) | `open` schedules a reminder with `WakeAt`; `remind` handles it ([`main.go`](main.go) exports both functions) |
| Storage | `loan:<id>` for each open loan and `open` for the count; a retried event is not counted twice |
| Customer details | `customer.name` always; `customer.phone` only when the shop turns on "Include customer phone numbers" |
| Network | The host of the URL the shop enters (`{setting:url}`), and `ntfy.sh` for phone notifications |
| Settings | Required (`url`), secret (`token`), with a default (`reminder_days`), on/off (`share_phone`), advanced (`ntfy_topic`) |
| Outcomes | `ok`; `blocked` for a missing URL, bad days or a refused token; `retry` with a backoff from the attempt number; `failed` for other refusals |
| Logs | When a reminder is scheduled, or could not be |
| Delivery | `at_least_once`, with the delivery id sent as `Idempotency-Key` so the service can drop a duplicate |

`http_hosts` can also name a wildcard (`*.example.com`) or an address on the shop's network with a port
(`192.168.1.20:8080`); see the [contract](../../plugin/contract/CONTRACT.md#http_hosts).

```sh
go test ./...
girvi plugin build
girvi plugin test --set url=https://example.com/in --set ntfy_topic=my-shop
girvi plugin test --kind wake --wake <loan id>
```
