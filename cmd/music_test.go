package cmd

import (
	"path/filepath"
	"testing"
)

// resolveMusicOutputPath is the small derivation we want to pin: the
// default output lives at /workspace/documents/music/<slug>.mp3 unless
// an explicit --output overrides it. Test the derivation so we notice
// if someone later flips the path convention.
func TestResolveMusicOutputPath(t *testing.T) {
	cases := []struct {
		name     string
		slug     string
		explicit string
		want     string
	}{
		{
			name: "default path from slug",
			slug: "signature-contemplative",
			want: filepath.Join("/workspace", "documents", "music", "signature-contemplative.mp3"),
		},
		{
			name:     "explicit output wins",
			slug:     "anything",
			explicit: "/tmp/custom/elsewhere.mp3",
			want:     "/tmp/custom/elsewhere.mp3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveMusicOutputPath(tc.slug, tc.explicit)
			if got != tc.want {
				t.Errorf("resolveMusicOutputPath(%q, %q) = %q, want %q", tc.slug, tc.explicit, got, tc.want)
			}
		})
	}
}

// Slug validation is already covered by the video tests (shared
// validateSlug helper), but repeat the music-specific cases here so the
// test names document the canonical-name policy we rely on in the
// music pipeline.
func TestMusicSlugValidation(t *testing.T) {
	cases := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		{name: "valid kebab signature", slug: "signature-contemplative", wantErr: false},
		{name: "valid short", slug: "sc", wantErr: false},
		{name: "empty rejected", slug: "", wantErr: true},
		{name: "too short rejected", slug: "a", wantErr: true},
		{name: "uppercase rejected", slug: "Signature", wantErr: true},
		{name: "leading dash rejected", slug: "-signature", wantErr: true},
		{name: "trailing dash rejected", slug: "signature-", wantErr: true},
		{name: "underscore rejected", slug: "signature_x", wantErr: true},
		{name: "space rejected", slug: "signature x", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSlug(tc.slug)
			if tc.wantErr && err == nil {
				t.Errorf("validateSlug(%q) expected error, got nil", tc.slug)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateSlug(%q) unexpected error: %v", tc.slug, err)
			}
		})
	}
}

// startsWith is a tiny helper in music.go used to gate on the audio
// response. Cover it directly so a future refactor doesn't flip
// semantics quietly.
func TestStartsWith(t *testing.T) {
	cases := []struct {
		s, prefix string
		want      bool
	}{
		{"audio/mpeg", "audio/", true},
		{"audio/mpeg; charset=binary", "audio/", true},
		{"application/json", "audio/", false},
		{"", "audio/", false},
		{"audio/", "audio/", true},
	}
	for _, tc := range cases {
		if got := startsWith(tc.s, tc.prefix); got != tc.want {
			t.Errorf("startsWith(%q, %q) = %v, want %v", tc.s, tc.prefix, got, tc.want)
		}
	}
}
