package config

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	ticketCredentialKeyPrefix   = "TICKET_CREDENTIAL_KEY_"
	ticketCredentialVersionsEnv = "TICKET_CREDENTIAL_KEY_VERSIONS"
	ticketCredentialVersionEnv  = "TICKET_CREDENTIAL_CURRENT_KEY_VERSION"
)

// TicketCredentialKeyring holds AES-256 keys used to encrypt and reveal ticket
// QR credentials. Keep these values outside PostgreSQL and source control.
type TicketCredentialKeyring struct {
	Keys           map[int16][]byte
	CurrentVersion int16
}

// TicketCredentialKeyringFromEnv reads only the versioned key variables named
// in TICKET_CREDENTIAL_KEY_VERSIONS and the version used for new credentials.
func TicketCredentialKeyringFromEnv() (TicketCredentialKeyring, error) {
	currentVersionValue, err := RequireEnv(ticketCredentialVersionEnv)
	if err != nil {
		return TicketCredentialKeyring{}, err
	}

	currentVersion64, err := strconv.ParseInt(currentVersionValue, 10, 16)
	if err != nil || currentVersion64 <= 0 {
		return TicketCredentialKeyring{}, fmt.Errorf("%s must be a positive 16-bit integer", ticketCredentialVersionEnv)
	}
	currentVersion := int16(currentVersion64)

	versionsValue, err := RequireEnv(ticketCredentialVersionsEnv)
	if err != nil {
		return TicketCredentialKeyring{}, err
	}
	versionValues := strings.Split(versionsValue, ",")
	if len(versionValues) == 0 {
		return TicketCredentialKeyring{}, fmt.Errorf("%s must list at least one key version", ticketCredentialVersionsEnv)
	}

	keys := make(map[int16][]byte)
	for _, rawVersion := range versionValues {
		versionValue := strings.TrimSpace(rawVersion)
		version64, err := strconv.ParseInt(versionValue, 10, 16)
		if err != nil || version64 <= 0 {
			clearTicketCredentialKeys(keys)
			return TicketCredentialKeyring{}, fmt.Errorf("%s must contain comma-separated positive 16-bit integers", ticketCredentialVersionsEnv)
		}
		version := int16(version64)
		if _, exists := keys[version]; exists {
			clearTicketCredentialKeys(keys)
			return TicketCredentialKeyring{}, fmt.Errorf("duplicate ticket credential key version %d", version)
		}

		name := fmt.Sprintf("%s%d", ticketCredentialKeyPrefix, version)
		encodedKey, err := RequireEnv(name)
		if err != nil {
			clearTicketCredentialKeys(keys)
			return TicketCredentialKeyring{}, err
		}
		key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
		if err != nil || len(key) != 32 {
			clear(key)
			clearTicketCredentialKeys(keys)
			return TicketCredentialKeyring{}, fmt.Errorf("%s must contain a base64-encoded 32-byte AES-256 key", name)
		}
		keys[version] = key
	}

	if _, ok := keys[currentVersion]; !ok {
		clearTicketCredentialKeys(keys)
		return TicketCredentialKeyring{}, fmt.Errorf("%s=%d must appear in %s", ticketCredentialVersionEnv, currentVersion, ticketCredentialVersionsEnv)
	}

	return TicketCredentialKeyring{Keys: keys, CurrentVersion: currentVersion}, nil
}

func clearTicketCredentialKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}
