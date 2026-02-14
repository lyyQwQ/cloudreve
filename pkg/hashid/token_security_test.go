package hashid_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
)

func TestShareTokenSecurityMatrix(t *testing.T) {
	encoder, err := hashid.New("todo12b-token-security-test-salt")
	if err != nil {
		t.Fatalf("new hashid encoder: %v", err)
	}

	const expectedID = 42
	validToken := hashid.EncodeShareID(encoder, expectedID)
	tamperedToken := tamperOneChar(validToken)

	past := time.Unix(1, 0)

	tests := []struct {
		name          string
		token         string
		expectID      int
		expectValid   bool
		expiredShare  *ent.Share
		expectExpired bool
	}{
		{
			name:        "valid hashid token",
			token:       validToken,
			expectID:    expectedID,
			expectValid: true,
		},
		{
			name:        "tampered one char",
			token:       tamperedToken,
			expectID:    expectedID,
			expectValid: false,
		},
		{
			name:        "random string",
			token:       "not-a-valid-token",
			expectID:    expectedID,
			expectValid: false,
		},
		{
			name:        "empty token",
			token:       "",
			expectID:    expectedID,
			expectValid: false,
		},
		{
			name:        "overly long token",
			token:       strings.Repeat("x", 256),
			expectID:    expectedID,
			expectValid: false,
		},
		{
			name:          "valid token but share expired",
			token:         validToken,
			expectID:      expectedID,
			expectValid:   true,
			expiredShare:  &ent.Share{Expires: &past},
			expectExpired: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decodedID, decodeErr := encoder.Decode(tc.token, hashid.ShareID)

			isValid := decodeErr == nil && decodedID == tc.expectID
			if tc.expectValid && !isValid {
				t.Fatalf("expected valid token, got decodedID=%d decodeErr=%v", decodedID, decodeErr)
			}
			if !tc.expectValid && isValid {
				t.Fatalf("expected invalid token, got decodedID=%d", decodedID)
			}

			if tc.expectExpired {
				if tc.expiredShare == nil {
					t.Fatal("expiredShare must be provided when expectExpired is true")
				}
				err := inventory.IsShareExpired(tc.expiredShare)
				if !errors.Is(err, inventory.ErrShareLinkExpired) {
					t.Fatalf("expected ErrShareLinkExpired, got %v", err)
				}
			}
		})
	}
}

func tamperOneChar(token string) string {
	if token == "" {
		return "a"
	}

	b := []byte(token)
	if b[0] == 'a' {
		b[0] = 'b'
	} else {
		b[0] = 'a'
	}

	return string(b)
}
