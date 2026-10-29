# Go HTTP rate limiting resources

## Knowledge

- [Go: net/http Handler](https://pkg.go.dev/net/http#Handler) — the handler contract and request/response lifecycle; use for middleware.
- [Go: sync.Mutex](https://pkg.go.dev/sync#Mutex) — lock and unlock shared state; use when protecting the limiter's map.
- [Go: net.SplitHostPort](https://pkg.go.dev/net#SplitHostPort) — extract the host from IPv4 and IPv6 host:port addresses.
- [Go: net/http/httptest](https://pkg.go.dev/net/http/httptest) — create HTTP requests and record responses in handler tests.
- [Go: race detector](https://go.dev/doc/articles/race_detector) — detect data races in concurrent Go code exercised by tests.
- [Go: time.Time.Add](https://pkg.go.dev/time#Time.Add) — compute time-window cutoffs.

## Wisdom

- The [Go community](https://go.dev/community) lists the official forum and other places to ask implementation questions.
