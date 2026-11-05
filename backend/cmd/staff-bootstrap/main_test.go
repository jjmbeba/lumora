package main

import (
	"strings"
	"testing"
)

func TestBootstrapSecretMatches(t *testing.T) {
	expected := strings.Repeat(string(rune('a'+len(t.Name())%26)), 32)
	if !bootstrapSecretMatches(expected, []byte(expected)) {
		t.Fatal("matching bootstrap secret was rejected")
	}

	wrong := []byte(expected)
	wrong[0]++
	if bootstrapSecretMatches(expected, wrong) {
		t.Fatal("incorrect bootstrap secret was accepted")
	}
	if bootstrapSecretMatches(expected, []byte(expected[:len(expected)-1])) {
		t.Fatal("bootstrap secret with a different length was accepted")
	}
}
