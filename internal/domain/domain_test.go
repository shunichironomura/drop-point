package domain

import (
	"errors"
	"testing"
	"time"
)

func validCreateParams() CreateParams {
	now := time.Unix(1_700_000_000, 0).UTC()
	return CreateParams{
		ID:              "dp_abc",
		APITokenID:      "desktop-main",
		ClientName:      "generic-client",
		DropTokenHash:   "sha256:drop",
		PickupTokenHash: "sha256:pick",
		MaxBytes:        1024,
		CreatedAt:       now,
		ExpiresAt:       now.Add(10 * time.Minute),
	}
}

func TestCreateParamsValidateAcceptsValid(t *testing.T) {
	if err := validCreateParams().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestCreateParamsValidateRejects(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name   string
		mutate func(*CreateParams)
	}{
		{"empty id", func(p *CreateParams) { p.ID = "" }},
		{"empty api token id", func(p *CreateParams) { p.APITokenID = "" }},
		{"empty drop hash", func(p *CreateParams) { p.DropTokenHash = "" }},
		{"empty pickup hash", func(p *CreateParams) { p.PickupTokenHash = "" }},
		{"zero max bytes", func(p *CreateParams) { p.MaxBytes = 0 }},
		{"negative max bytes", func(p *CreateParams) { p.MaxBytes = -1 }},
		{"zero created_at", func(p *CreateParams) { p.CreatedAt = time.Time{} }},
		{"zero expires_at", func(p *CreateParams) { p.ExpiresAt = time.Time{} }},
		{"expires before created", func(p *CreateParams) { p.ExpiresAt = now.Add(-time.Second) }},
		{"expires equals created", func(p *CreateParams) { p.ExpiresAt = p.CreatedAt }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validCreateParams()
			tt.mutate(&p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !errors.Is(err, ErrInvalidParams) {
				t.Errorf("Validate() error = %v, want wrapping ErrInvalidParams", err)
			}
		})
	}
}

func TestCommitParamsValidate(t *testing.T) {
	valid := CommitParams{
		ID:            "dp_abc",
		PayloadPath:   "drop-points/dp_abc/payload.bin",
		EnvelopePath:  "drop-points/dp_abc/envelope.json",
		EncryptedSize: 2048,
		DroppedAt:     time.Unix(1_700_000_100, 0).UTC(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	tests := []struct {
		name   string
		mutate func(*CommitParams)
	}{
		{"empty id", func(p *CommitParams) { p.ID = "" }},
		{"empty payload path", func(p *CommitParams) { p.PayloadPath = "" }},
		{"empty envelope path", func(p *CommitParams) { p.EnvelopePath = "" }},
		{"negative size", func(p *CommitParams) { p.EncryptedSize = -1 }},
		{"zero dropped_at", func(p *CommitParams) { p.DroppedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.mutate(&p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !errors.Is(err, ErrInvalidParams) {
				t.Errorf("Validate() error = %v, want wrapping ErrInvalidParams", err)
			}
		})
	}

	// Zero is a legitimate encrypted size (an empty bundle is still authenticated).
	zero := valid
	zero.EncryptedSize = 0
	if err := zero.Validate(); err != nil {
		t.Errorf("Validate() with zero size = %v, want nil", err)
	}
}

func TestExpiredAt(t *testing.T) {
	exp := time.Unix(1_700_000_600, 0).UTC()
	dp := &DropPoint{ExpiresAt: exp}

	if dp.ExpiredAt(exp.Add(-time.Second)) {
		t.Error("ExpiredAt before expiry = true, want false")
	}
	if !dp.ExpiredAt(exp) {
		t.Error("ExpiredAt exactly at expiry = false, want true")
	}
	if !dp.ExpiredAt(exp.Add(time.Second)) {
		t.Error("ExpiredAt after expiry = false, want true")
	}
}
