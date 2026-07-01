package dropname

import "testing"

func TestGenerateReturnsAdjectiveNounName(t *testing.T) {
	name, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !Valid(name) {
		t.Fatalf("generated name = %q, want adjective-noun", name)
	}
}

func TestValid(t *testing.T) {
	tests := map[string]bool{
		"calm-otter":       true,
		"massive-colt":     true,
		"":                 false,
		"calm":             false,
		"calm_otter":       false,
		"Calm-Otter":       false,
		"calm-otter-7":     false,
		"<script>-otter":   false,
		"calm-otter\nnext": false,
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Valid(name); got != want {
				t.Fatalf("Valid(%q) = %v, want %v", name, got, want)
			}
		})
	}
}
