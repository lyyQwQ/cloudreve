package mediameta

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver/local"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager/entitysource"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

type staticSettingAdapter struct {
	values map[string]any
}

func (s *staticSettingAdapter) Get(_ context.Context, name string, defaultVal any) any {
	if val, ok := s.values[name]; ok {
		return val
	}

	return defaultVal
}

type fakeLocalEntitySource struct {
	entity    fs.Entity
	localPath string
	file      *os.File
}

func newFakeLocalEntitySource(t *testing.T, rawPath string) *fakeLocalEntitySource {
	t.Helper()

	cleanPath := filepath.Clean(rawPath)
	entity, err := local.NewLocalFileEntity(types.EntityTypeVersion, cleanPath)
	if err != nil {
		t.Fatalf("create local entity: %v", err)
	}

	fd, err := os.Open(cleanPath)
	if err != nil {
		t.Fatalf("open local file: %v", err)
	}

	return &fakeLocalEntitySource{
		entity:    entity,
		localPath: rawPath,
		file:      fd,
	}
}

func (s *fakeLocalEntitySource) Read(p []byte) (int, error) {
	return s.file.Read(p)
}

func (s *fakeLocalEntitySource) ReadAt(p []byte, off int64) (int, error) {
	return s.file.ReadAt(p, off)
}

func (s *fakeLocalEntitySource) Seek(offset int64, whence int) (int64, error) {
	return s.file.Seek(offset, whence)
}

func (s *fakeLocalEntitySource) Close() error {
	return s.file.Close()
}

func (s *fakeLocalEntitySource) Url(_ context.Context, _ ...entitysource.EntitySourceOption) (*entitysource.EntityUrl, error) {
	return &entitysource.EntityUrl{Url: "https://example.com/fake.mp4"}, nil
}

func (s *fakeLocalEntitySource) Serve(_ http.ResponseWriter, _ *http.Request, _ ...entitysource.EntitySourceOption) {
}

func (s *fakeLocalEntitySource) Entity() fs.Entity {
	return s.entity
}

func (s *fakeLocalEntitySource) IsLocal() bool {
	return true
}

func (s *fakeLocalEntitySource) LocalPath(_ context.Context) string {
	return s.localPath
}

func (s *fakeLocalEntitySource) Apply(_ ...entitysource.EntitySourceOption) {}

func (s *fakeLocalEntitySource) CloneToLocalSrc(_ types.EntityType, _ string) (entitysource.EntitySource, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *fakeLocalEntitySource) ShouldInternalProxy(_ ...entitysource.EntitySourceOption) bool {
	return false
}

func TestFFProbeCommandInjectionMatrix(t *testing.T) {
	testCases := []struct {
		name      string
		malicious string
	}{
		{name: "semicolon", malicious: "video;rm -rf /tmp.mp4"},
		{name: "dollar_subshell", malicious: "video$(whoami).mp4"},
		{name: "backtick_subshell", malicious: "video`id`.mp4"},
		{name: "pipe", malicious: "video|cat /etc/passwd.mp4"},
		{name: "fake_input_flag", malicious: "-i /etc/passwd -o out.mp4"},
		{name: "double_dash", malicious: "--help.mp4"},
		{name: "filter_payload", malicious: "-vf \"subtitles='/etc/passwd'\""},
		{name: "traversal", malicious: "../../../etc/passwd.mp4"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			binDir := filepath.Join(workDir, "bin")
			argsFile := filepath.Join(workDir, "ffprobe.argv")
			if err := os.MkdirAll(binDir, 0755); err != nil {
				t.Fatalf("create bin dir: %v", err)
			}

			script := strings.Join([]string{
				"#!/bin/sh",
				"printf '%s\\n' \"$@\" > \"" + argsFile + "\"",
				"cat <<'EOF'",
				"{\"format\":{\"format_name\":\"mp4\"},\"streams\":[],\"chapters\":[]}",
				"EOF",
				"exit 0",
			}, "\n") + "\n"

			ffprobePath := filepath.Join(binDir, "ffprobe")
			if err := os.WriteFile(ffprobePath, []byte(script), 0755); err != nil {
				t.Fatalf("write fake ffprobe: %v", err)
			}

			rawPath, cleanPath := createMaliciousInputPath(t, workDir, tc.malicious)
			source := newFakeLocalEntitySource(t, rawPath)
			defer func() {
				_ = source.Close()
			}()

			settingsProvider := setting.NewProvider(&staticSettingAdapter{values: map[string]any{
				"media_meta_ffprobe_path": ffprobePath,
			}})
			extractor := newFFProbeExtractor(settingsProvider, logging.NewConsoleLogger(logging.LevelError))

			res, err := extractor.Extract(context.Background(), "mp4", source)
			if err != nil {
				t.Fatalf("extract should not fail, path=%q cleanPath=%q err=%v", rawPath, cleanPath, err)
			}
			if len(res) == 0 {
				t.Fatalf("expected non-empty metadata for path %q", rawPath)
			}

			argvRaw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("read captured argv: %v", err)
			}

			argv := parseArgvLines(string(argvRaw))
			if len(argv) != 8 {
				t.Fatalf("expected 8 ffprobe args, got %d (%q)", len(argv), argv)
			}

			expectedPrefix := []string{"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", "-show_chapters"}
			for i, want := range expectedPrefix {
				if argv[i] != want {
					t.Fatalf("arg[%d] mismatch: want %q, got %q", i, want, argv[i])
				}
			}

			if argv[7] != rawPath {
				t.Fatalf("input path should stay as single argv item, want %q, got %q", rawPath, argv[7])
			}
		})
	}
}

func createMaliciousInputPath(t *testing.T, root, payload string) (string, string) {
	t.Helper()

	prefix := filepath.Join(root, "inputs", "x", "y", "z")
	if err := os.MkdirAll(prefix, 0755); err != nil {
		t.Fatalf("create input prefix dirs: %v", err)
	}

	rawPath := prefix + string(os.PathSeparator) + payload
	cleanPath := filepath.Clean(rawPath)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0755); err != nil {
		t.Fatalf("create input parent dirs: %v", err)
	}
	if err := os.WriteFile(cleanPath, []byte("video"), 0600); err != nil {
		t.Fatalf("write malicious input fixture: %v", err)
	}

	return rawPath, cleanPath
}

func parseArgvLines(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	return strings.Split(trimmed, "\n")
}
