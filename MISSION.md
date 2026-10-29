# Mission: Implement the booking verification rate limiter

## Why
Protect the cinema booking email-verification endpoints while learning enough Go HTTP and concurrency to build and maintain the limiter independently.

## Success looks like
- Explain how a request moves through middleware and how an IP identifies a rate-limit bucket.
- Implement and test a concurrency-safe rolling limit of 20 requests per IP per hour.

## Constraints
- The learner knows basic Go syntax and pure functions and has experience in other languages; Go HTTP servers are new.
- Keep lessons concise, practical, and connected to the existing booking API.

## Out of scope
- Distributed rate limiting and unconfigured proxy trust for the first single-instance version.
