package postgres

import "errors"

var (
	ErrDatabasePoolRequired           = errors.New("database pool is required")
	ErrTicketCredentialCipherRequired = errors.New("ticket credential cipher is required")
)
