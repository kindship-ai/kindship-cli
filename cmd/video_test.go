package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// createVideoArchive packs a Remotion bundle tarball. This test locks in the
// canonical-lowercase `poster.png` contract added in v0.1.49 so we don't
// silently regress to packing random image files or forgetting the poster
// entirely on future edits.
func TestCreateVideoArchivePoster(t *testing.T) {
	tests := []struct {
		name        string
		extraFiles  map[string]string // path (relative to dir) → content
		wantMembers []string          // expected archive members (unordered OK)
		notMembers  []string          // members that must NOT be in the archive
	}{
		{
			name:        "no poster: archive has only composition.mjs + compositions.json",
			extraFiles:  nil,
			wantMembers: []string{"composition.mjs", "compositions.json"},
			notMembers:  []string{"poster.png", "poster.jpg", "poster.webp"},
		},
		{
			name: "poster.png present: included at archive root",
			extraFiles: map[string]string{
				"poster.png": "\x89PNG\r\n\x1a\n-fake-",
			},
			wantMembers: []string{"composition.mjs", "compositions.json", "poster.png"},
			notMembers:  []string{"poster.jpg", "poster.webp"},
		},
		{
			name: "poster.jpg alone (no poster.png): ignored per single-canonical policy",
			extraFiles: map[string]string{
				"poster.jpg": "\xff\xd8\xff\xe0-fake-",
			},
			wantMembers: []string{"composition.mjs", "compositions.json"},
			notMembers:  []string{"poster.png", "poster.jpg", "poster.webp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Scratch dir laid out like /workspace/videos/<slug>/: a minimal
			// composition.mjs + compositions.json plus whatever the case
			// says should be there.
			dir := t.TempDir()
			compMjs := filepath.Join(dir, "composition.mjs")
			compJSON := filepath.Join(dir, "compositions.json")

			if err := os.WriteFile(compMjs, []byte("export default () => null;"), 0o644); err != nil {
				t.Fatalf("write composition.mjs: %v", err)
			}
			if err := os.WriteFile(compJSON, []byte(`[{"id":"t","durationInFrames":1,"fps":30,"width":16,"height":9}]`), 0o644); err != nil {
				t.Fatalf("write compositions.json: %v", err)
			}
			for rel, content := range tt.extraFiles {
				if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
					t.Fatalf("write %s: %v", rel, err)
				}
			}

			// publicDir is intentionally missing — this test doesn't exercise
			// the public/ walk, just the canonical-name + optional poster
			// logic. createVideoArchive gracefully skips a non-existent dir.
			publicDir := filepath.Join(dir, "public")

			buf, fileCount, err := createVideoArchive(compMjs, compJSON, publicDir)
			if err != nil {
				t.Fatalf("createVideoArchive: %v", err)
			}

			members := readTarGzMembers(t, buf)
			memberSet := make(map[string]bool, len(members))
			for _, m := range members {
				memberSet[m] = true
			}

			for _, want := range tt.wantMembers {
				if !memberSet[want] {
					t.Errorf("expected member %q in archive, got members: %v", want, members)
				}
			}
			for _, avoid := range tt.notMembers {
				if memberSet[avoid] {
					t.Errorf("unexpected member %q in archive, got members: %v", avoid, members)
				}
			}

			// fileCount should track the actual archive member count.
			if fileCount != len(members) {
				t.Errorf("fileCount = %d but archive has %d members (%v)", fileCount, len(members), members)
			}
		})
	}
}

// Music bundling lives in createVideoArchive alongside the poster
// probe — exercised here so the signature-audio sidecar contract is
// locked in. Music files flow at archive root under "music/<...>"
// (sibling of composition.mjs), NOT nested under public/music, because
// compositions resolve audio via new URL(import.meta.url)-relative
// paths and the file must be a sibling of composition.mjs.
func TestCreateVideoArchiveMusic(t *testing.T) {
	tests := []struct {
		name        string
		musicFiles  map[string]string // relative path under music/ → content
		wantMembers []string          // expected archive members
		notMembers  []string
	}{
		{
			name:        "no music dir: archive omits music entries",
			musicFiles:  nil,
			wantMembers: []string{"composition.mjs", "compositions.json"},
			notMembers:  []string{"music/signature.mp3", "public/music/signature.mp3"},
		},
		{
			name: "single track: packed at archive root music/",
			musicFiles: map[string]string{
				"signature-contemplative.mp3": "\xff\xfb-fake-mp3-",
			},
			wantMembers: []string{
				"composition.mjs",
				"compositions.json",
				"music/signature-contemplative.mp3",
			},
			notMembers: []string{"public/music/signature-contemplative.mp3"},
		},
		{
			name: "multiple tracks: all packed",
			musicFiles: map[string]string{
				"signature-contemplative.mp3": "\xff\xfb-fake-a-",
				"signature-kinetic.mp3":       "\xff\xfb-fake-b-",
			},
			wantMembers: []string{
				"composition.mjs",
				"compositions.json",
				"music/signature-contemplative.mp3",
				"music/signature-kinetic.mp3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			compMjs := filepath.Join(dir, "composition.mjs")
			compJSON := filepath.Join(dir, "compositions.json")
			if err := os.WriteFile(compMjs, []byte("export default () => null;"), 0o644); err != nil {
				t.Fatalf("write composition.mjs: %v", err)
			}
			if err := os.WriteFile(compJSON, []byte(`[{"id":"t","durationInFrames":1,"fps":30,"width":16,"height":9}]`), 0o644); err != nil {
				t.Fatalf("write compositions.json: %v", err)
			}

			if tt.musicFiles != nil {
				musicDir := filepath.Join(dir, "music")
				if err := os.MkdirAll(musicDir, 0o755); err != nil {
					t.Fatalf("mkdir music: %v", err)
				}
				for rel, content := range tt.musicFiles {
					if err := os.WriteFile(filepath.Join(musicDir, rel), []byte(content), 0o644); err != nil {
						t.Fatalf("write music/%s: %v", rel, err)
					}
				}
			}

			publicDir := filepath.Join(dir, "public")

			buf, fileCount, err := createVideoArchive(compMjs, compJSON, publicDir)
			if err != nil {
				t.Fatalf("createVideoArchive: %v", err)
			}

			members := readTarGzMembers(t, buf)
			memberSet := make(map[string]bool, len(members))
			for _, m := range members {
				memberSet[m] = true
			}
			for _, want := range tt.wantMembers {
				if !memberSet[want] {
					t.Errorf("expected member %q, got %v", want, members)
				}
			}
			for _, avoid := range tt.notMembers {
				if memberSet[avoid] {
					t.Errorf("unexpected member %q, got %v", avoid, members)
				}
			}
			if fileCount != len(members) {
				t.Errorf("fileCount = %d but archive has %d members (%v)", fileCount, len(members), members)
			}
		})
	}
}

// TestCreateVideoArchiveSite verifies the optional Lambda-render webpack
// bundle directory is packed at archive root under "site/<...>", mirroring
// the public/ + music/ pattern. site/ is what powers the Download MP4
// button in the Videos tab; absence is silent (publish + the in-browser
// player work without it).
func TestCreateVideoArchiveSite(t *testing.T) {
	tests := []struct {
		name        string
		siteFiles   map[string]string // relative path under site/ → content
		wantMembers []string
		notMembers  []string
	}{
		{
			name:        "no site dir: archive omits site entries",
			siteFiles:   nil,
			wantMembers: []string{"composition.mjs", "compositions.json"},
			notMembers:  []string{"site/index.html", "site/static/js/main.js"},
		},
		{
			name: "flat site bundle: index.html + js chunk packed at archive root",
			siteFiles: map[string]string{
				"index.html":         "<!doctype html><html></html>",
				"bundle.js":          "/* webpack chunk */",
			},
			wantMembers: []string{
				"composition.mjs",
				"compositions.json",
				"site/index.html",
				"site/bundle.js",
			},
			notMembers: []string{"index.html", "bundle.js"},
		},
		{
			name: "nested site bundle: subdirectories preserved under site/",
			siteFiles: map[string]string{
				"index.html":            "<!doctype html><html></html>",
				"static/js/main.js":     "/* main chunk */",
				"static/js/runtime.js":  "/* runtime */",
				"static/css/style.css":  ".x{}",
				"static/media/font.woff": "fakefont",
			},
			wantMembers: []string{
				"composition.mjs",
				"compositions.json",
				"site/index.html",
				"site/static/js/main.js",
				"site/static/js/runtime.js",
				"site/static/css/style.css",
				"site/static/media/font.woff",
			},
			notMembers: []string{"index.html", "static/js/main.js"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			compMjs := filepath.Join(dir, "composition.mjs")
			compJSON := filepath.Join(dir, "compositions.json")
			if err := os.WriteFile(compMjs, []byte("export default () => null;"), 0o644); err != nil {
				t.Fatalf("write composition.mjs: %v", err)
			}
			if err := os.WriteFile(compJSON, []byte(`[{"id":"t","durationInFrames":1,"fps":30,"width":16,"height":9}]`), 0o644); err != nil {
				t.Fatalf("write compositions.json: %v", err)
			}

			if tt.siteFiles != nil {
				for rel, content := range tt.siteFiles {
					full := filepath.Join(dir, "site", rel)
					if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
						t.Fatalf("mkdir for site/%s: %v", rel, err)
					}
					if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
						t.Fatalf("write site/%s: %v", rel, err)
					}
				}
			}

			publicDir := filepath.Join(dir, "public")

			buf, fileCount, err := createVideoArchive(compMjs, compJSON, publicDir)
			if err != nil {
				t.Fatalf("createVideoArchive: %v", err)
			}

			members := readTarGzMembers(t, buf)
			memberSet := make(map[string]bool, len(members))
			for _, m := range members {
				memberSet[m] = true
			}
			for _, want := range tt.wantMembers {
				if !memberSet[want] {
					t.Errorf("expected member %q, got %v", want, members)
				}
			}
			for _, avoid := range tt.notMembers {
				if memberSet[avoid] {
					t.Errorf("unexpected member %q, got %v", avoid, members)
				}
			}
			if fileCount != len(members) {
				t.Errorf("fileCount = %d but archive has %d members (%v)", fileCount, len(members), members)
			}
		})
	}
}

// readTarGzMembers returns the names of every regular file inside a
// gzip-compressed tarball. Order preserved (tarball is small enough that
// tests compare via a member-set, not order).
func readTarGzMembers(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	gr, err := gzip.NewReader(buf)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	var members []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tr.Next: %v", err)
		}
		members = append(members, hdr.Name)
	}
	return members
}
