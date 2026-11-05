package config

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	staffInvitationKeyPrefix   = "STAFF_INVITATION_KEY_"
	staffInvitationVersionsEnv = "STAFF_INVITATION_KEY_VERSIONS"
	staffInvitationCurrentEnv  = "STAFF_INVITATION_CURRENT_KEY_VERSION"
)

type StaffInvitationKeyring struct {
	Keys           map[int16][]byte
	CurrentVersion int16
}

func StaffInvitationKeyringFromEnv() (StaffInvitationKeyring, error) {
	currentValue, err := RequireEnv(staffInvitationCurrentEnv)
	if err != nil {
		return StaffInvitationKeyring{}, err
	}

	currentVersion, err := strconv.ParseInt(currentValue, 10, 16)
	if err != nil || currentVersion <= 0 {
		return StaffInvitationKeyring{}, fmt.Errorf("%s must be a positive 16-bit integer", staffInvitationCurrentEnv)
	}

	versionsValue, err := RequireEnv(staffInvitationVersionsEnv)
	if err != nil {
		return StaffInvitationKeyring{}, err
	}
	keys := make(map[int16][]byte)
	for rawVersion := range strings.SplitSeq(versionsValue, ",") {
		version64, err := strconv.ParseInt(strings.TrimSpace(rawVersion), 10, 16)
		if err != nil || version64 <= 0 {
			clearStaffInvitationKeys(keys)
			return StaffInvitationKeyring{}, fmt.Errorf("%s must contain comma-separated positive 16-bit integers", staffInvitationVersionsEnv)
		}
		version := int16(version64)
		if _, exists := keys[version]; exists {
			clearStaffInvitationKeys(keys)
			return StaffInvitationKeyring{}, fmt.Errorf("duplicate staff invitation key version %d", version)
		}

		name := fmt.Sprintf("%s%d", staffInvitationKeyPrefix, version)
		encoded, err := RequireEnv(name)
		if err != nil {
			clearStaffInvitationKeys(keys)
			return StaffInvitationKeyring{}, err
		}
		key, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(key) != 32 {
			clear(key)
			clearStaffInvitationKeys(keys)
			return StaffInvitationKeyring{}, fmt.Errorf("%s must contain a base64-encoded 32-byte AES key", name)
		}
		keys[version] = key
	}

	current := int16(currentVersion)
	if _, ok := keys[current]; !ok {
		clearStaffInvitationKeys(keys)
		return StaffInvitationKeyring{}, fmt.Errorf("%s=%d must appear in %s", staffInvitationCurrentEnv, current, staffInvitationVersionsEnv)
	}
	return StaffInvitationKeyring{Keys: keys, CurrentVersion: current}, nil
}

func clearStaffInvitationKeys(keys map[int16][]byte) {
	for _, key := range keys {
		clear(key)
	}
}
