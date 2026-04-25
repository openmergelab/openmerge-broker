package model

import (
	"encoding/base64"
	"regexp"

	"github.com/uber/h3-go/v4"
)

var (
	uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	hex64Re  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

	validSeeking = map[string]bool{
		"M":   true,
		"F":   true,
		"NB":  true,
		"any": true,
	}

	validGender = map[string]bool{
		"M":   true,
		"F":   true,
		"NB":  true,
	}
)

func ValidateSignalRequest(r *SignalRequest) []string {
	var invalid []string

	// V-001: anonymousId must be valid UUID v4
	if !uuidV4Re.MatchString(r.AnonymousID) {
		invalid = append(invalid, "anonymousId")
	}

	// V-002: locationH3 must be valid H3 cell at resolution 9
	if !isValidH3Resolution9(r.LocationH3) {
		invalid = append(invalid, "locationH3")
	}

	// V-003: seeking must be one of the allowed values
	if !validSeeking[r.Seeking] {
		invalid = append(invalid, "seeking")
	}

	// V-003b: gender must be M, F, or NB
	if !validGender[r.Gender] {
		invalid = append(invalid, "gender")
	}

	// V-004: ageRange must be min >= 18, max <= 120, min <= max
	if r.AgeRange.Min < 18 || r.AgeRange.Max > 120 || r.AgeRange.Min > r.AgeRange.Max {
		invalid = append(invalid, "ageRange")
	}

	// V-004b: age must be >= 18 and <= 120
	if r.Age < 18 || r.Age > 120 {
		invalid = append(invalid, "age")
	}

	// V-005: publicKey must be 64-character hex string
	if !hex64Re.MatchString(r.PublicKey) {
		invalid = append(invalid, "publicKey")
	}

	// V-006: encryptedVector must be non-empty valid base64
	if r.EncryptedVector == "" {
		invalid = append(invalid, "encryptedVector")
	} else if _, err := base64.StdEncoding.DecodeString(r.EncryptedVector); err != nil {
		invalid = append(invalid, "encryptedVector")
	}

	// V-007: discordIdHash must be 64-character hex string
	if !hex64Re.MatchString(r.DiscordIDHash) {
		invalid = append(invalid, "discordIdHash")
	}

	// V-008 & V-009: handled at parse level (JSON decode) and unknown fields ignored

	return invalid
}

func isValidH3Resolution9(s string) bool {
	if s == "" {
		return false
	}
	cell := h3.CellFromString(s)
	return cell.IsValid() && cell.Resolution() == 9
}
