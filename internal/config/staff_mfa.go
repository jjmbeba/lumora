package config

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	staffMFAKeyPrefix   = "STAFF_MFA_KEY_"
	staffMFAVersionsEnv = "STAFF_MFA_KEY_VERSIONS"
	staffMFACurrentEnv  = "STAFF_MFA_CURRENT_KEY_VERSION"
)

type StaffMFAKeyring struct {
	Keys           map[int16][]byte
	CurrentVersion int16
}

func StaffMFAKeyringFromEnv() (StaffMFAKeyring, error) {
	currentValue, err := RequireEnv(staffMFACurrentEnv)
	if err != nil {
		return StaffMFAKeyring{}, err
	}

	current64, err := strconv.ParseInt(currentValue, 10, 16)
	if err != nil || current64 <= 0 {
		return StaffMFAKeyring{}, fmt.Errorf("%s must be a positive 16-bit integer", staffMFACurrentEnv)
	}

	versionsValue, err := RequireEnv(staffMFAVersionsEnv)
	if err != nil {
		return StaffMFAKeyring{}, err
	}

	keys := make(map[int16][]byte)
	for raw := range strings.SplitSeq(versionsValue, ",") {
		version64, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 16)
		if err != nil || version64 <= 0 {
			clearStaffMFAKeys(keys)
			return StaffMFAKeyring{}, fmt.Errorf("%s must contain comma-separated positive 16-bit integers", staffMFAVersionsEnv)
		}

		version := int16(version64)
		if _, exists := keys[version]; exists {
			clearStaffMFAKeys(keys)
			return StaffMFAKeyring{}, fmt.Errorf("duplicate staff MFA key version %d", version)
		}

		name := fmt.Sprintf("%s%d", staffMFAKeyPrefix, version)
		encoded, err := RequireEnv(name)
		if err != nil {
			clearStaffMFAKeys(keys)
			return StaffMFAKeyring{}, err
		}

		key, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(key) != 32 {
			clear(key)
			clearStaffMFAKeys(keys)
			return StaffMFAKeyring{}, fmt.Errorf("%s must contain a base64-encoded 32-byte AES key", name)
		}

		keys[version] = key
	}

	current := int16(current64)
	if _, ok := keys[current]; !ok {
		clearStaffMFAKeys(keys)

		return StaffMFAKeyring{}, fmt.Errorf("%s=%d must appear in %s", staffMFACurrentEnv, current, staffMFAVersionsEnv)
	}

	return StaffMFAKeyring{Keys: keys, CurrentVersion: current}, nil
}

func clearStaffMFAKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}
