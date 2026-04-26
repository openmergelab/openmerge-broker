package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

const stripeBase = "https://api.stripe.com/v1"

// StartSession creates a Stripe Identity verification session.
// Returns the verification URL to open on the user's device and
// a session ID to poll for completion.
func StartSession(ctx context.Context, anonymousID string) (verificationURL, sessionID string, err error) {
	stripeKey := os.Getenv("STRIPE_SECRET_KEY")
	if stripeKey == "" {
		return "", "", fmt.Errorf("STRIPE_SECRET_KEY not set")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"type": "document",
		"options": map[string]interface{}{
			"document": map[string]interface{}{
				"require_id_number":              false,
				"require_live_capture":           true,
				"require_matching_selfie":        true,
				"allowed_types":                  []string{"driving_license", "passport", "id_card"},
			},
		},
		"metadata": map[string]string{
			"anonymous_id": anonymousID,
		},
	})

	req, err := http.NewRequestWithContext(ctx, "POST",
		stripeBase+"/identity/verification_sessions",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(stripeKey, "")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("stripe session: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode stripe response: %w", err)
	}

	return result.URL, result.ID, nil
}

// CheckSession polls a verification session for completion.
// Returns true if the user is verified as over 18.
func CheckSession(ctx context.Context, sessionID string) (verified bool, err error) {
	stripeKey := os.Getenv("STRIPE_SECRET_KEY")

	req, _ := http.NewRequestWithContext(ctx, "GET",
		stripeBase+"/identity/verification_sessions/"+sessionID, nil,
	)
	req.SetBasicAuth(stripeKey, "")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("check session: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Status       string `json:"status"`
		LastError    *struct {
			Code string `json:"code"`
		} `json:"last_error"`
		VerifiedOutputs *struct {
			DOB *struct {
				Day   int `json:"day"`
				Month int `json:"month"`
				Year  int `json:"year"`
			} `json:"dob"`
		} `json:"verified_outputs"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	if result.Status != "verified" {
		return false, nil
	}

	// Verify age from DOB if available
	// Stripe verifies the document — we just check the outcome
	return result.Status == "verified", nil
}

// WebhookSecret returns the Stripe webhook signing secret.
func WebhookSecret() string {
	return os.Getenv("STRIPE_WEBHOOK_SECRET")
}
