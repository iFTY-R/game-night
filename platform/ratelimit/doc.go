// Package ratelimit defines the domain-facing, fail-closed contract for consuming rate-limit buckets.
// Storage-specific token bucket behavior and HMAC key derivation belong to persistence adapters.
package ratelimit
