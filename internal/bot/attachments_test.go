package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
)

func TestAppendAttachmentContextAddsSandboxPaths(t *testing.T) {
	got := appendAttachmentContext("Fix the UI", []sandboxAttachmentRef{{
		Filename:    "screenshot.png",
		Path:        "/tmp/hetchy-attachments/req/01-screenshot.png",
		ContentType: "image/png",
		SizeBytes:   1234,
	}})
	for _, want := range []string{
		"Fix the UI",
		"Attached files are available in the sandbox",
		"screenshot.png (image/png, 1234 bytes): /tmp/hetchy-attachments/req/01-screenshot.png",
		"manifest.json",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("attachment prompt missing %q:\n%s", want, got)
		}
	}
}

func TestMaterializePromptAttachmentsUploadsFilesAndManifest(t *testing.T) {
	uploaded := map[string]string{}
	var createdDir string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files/folder":
			if r.Method != http.MethodPost {
				t.Fatalf("folder method = %s, want POST", r.Method)
			}
			createdDir = r.URL.Query().Get("path")
			if got := r.URL.Query().Get("mode"); got != "0755" {
				t.Fatalf("folder mode = %q, want 0755", got)
			}
		case "/files/upload":
			if r.Method != http.MethodPost {
				t.Fatalf("upload method = %s, want POST", r.Method)
			}
			if err := r.ParseMultipartForm(1024 * 1024); err != nil {
				t.Fatalf("parse upload: %v", err)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("form file: %v", err)
			}
			body, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil {
				t.Fatalf("read form file: %v", err)
			}
			uploaded[r.URL.Query().Get("path")] = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	b := &Bot{log: discardLogger()}
	emit := newCaptureEmitter()
	refs, err := b.materializePromptAttachments(
		context.Background(),
		&daytona.Sandbox{FileSystem: newTestFileSystem(server.URL)},
		"Req 1",
		[]convstore.Attachment{{
			Filename:    "../log file.json",
			ContentType: "application/json",
			Data:        []byte(`{"ok":true}`),
		}},
		emit,
	)
	if err != nil {
		t.Fatalf("materializePromptAttachments: %v", err)
	}

	dir := sandboxAttachmentRoot + "/Req_1"
	if createdDir != dir {
		t.Fatalf("createdDir = %q, want %q", createdDir, dir)
	}
	attachmentPath := dir + "/01-log_file.json"
	if len(refs) != 1 || refs[0].Path != attachmentPath || refs[0].SizeBytes != int64(len(`{"ok":true}`)) {
		t.Fatalf("refs = %+v", refs)
	}
	if got := uploaded[attachmentPath]; got != `{"ok":true}` {
		t.Fatalf("uploaded attachment = %q", got)
	}
	var manifest struct {
		Files []sandboxAttachmentRef `json:"files"`
	}
	if err := json.Unmarshal([]byte(uploaded[dir+"/manifest.json"]), &manifest); err != nil {
		t.Fatalf("manifest json: %v body=%q", err, uploaded[dir+"/manifest.json"])
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Filename != "../log file.json" || manifest.Files[0].Path != attachmentPath {
		t.Fatalf("manifest files = %+v", manifest.Files)
	}
	if len(emit.Blocks) != 1 || emit.Blocks[0].Kind != blocks.KindNotify || emit.Blocks[0].Summary != "1 file(s)" {
		t.Fatalf("emitted blocks = %+v", emit.Blocks)
	}
}

func TestMaterializePromptAttachmentsRejectsMissingSandboxFilesystem(t *testing.T) {
	b := &Bot{log: discardLogger()}
	_, err := b.materializePromptAttachments(context.Background(), &daytona.Sandbox{}, "req", []convstore.Attachment{{
		Filename: "log.txt",
		Data:     []byte("hello"),
	}}, newCaptureEmitter())
	if err == nil || !strings.Contains(err.Error(), "sandbox file system is not available") {
		t.Fatalf("err = %v, want missing filesystem error", err)
	}
}

func TestSaveIncomingAttachmentsNormalizesMetadata(t *testing.T) {
	convs := &fakeConversationStore{}
	b := &Bot{log: discardLogger(), convs: convs}
	err := b.saveIncomingAttachments(context.Background(), "org-1", "thread-1", 2, []convstore.Attachment{{
		Filename: "",
		Data:     []byte("hello"),
	}})
	if err != nil {
		t.Fatalf("saveIncomingAttachments: %v", err)
	}

	got, err := convs.ListAttachments(context.Background(), "org-1", "thread-1")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(got), got)
	}
	a := got[0]
	if strings.TrimSpace(a.ID) == "" {
		t.Fatal("expected generated id")
	}
	if a.OrgID != "org-1" || a.ThreadID != "thread-1" || a.TurnIndex != 2 {
		t.Fatalf("conversation coordinates = %+v", a)
	}
	if a.Filename != "attachment" || a.ContentType != defaultAttachmentMimeType || a.Source != "web" {
		t.Fatalf("normalized metadata = %+v", a)
	}
	if a.SizeBytes != 5 || string(a.Data) != "hello" {
		t.Fatalf("stored payload = %+v data=%q", a, string(a.Data))
	}
}

func TestReplaceIncomingAttachmentsForTurnDropsStaleRetryFiles(t *testing.T) {
	convs := &fakeConversationStore{attachments: []convstore.Attachment{
		{ID: "old", OrgID: "org-1", ThreadID: "thread-1", TurnIndex: 0, Filename: "old.txt", Data: []byte("old")},
		{ID: "follow-up", OrgID: "org-1", ThreadID: "thread-1", TurnIndex: 1, Filename: "follow-up.txt", Data: []byte("later")},
	}}
	b := &Bot{log: discardLogger(), convs: convs}
	err := b.replaceIncomingAttachmentsForTurn(context.Background(), "org-1", "thread-1", 0, []convstore.Attachment{{
		Filename: "new.txt",
		Data:     []byte("new"),
	}})
	if err != nil {
		t.Fatalf("replaceIncomingAttachmentsForTurn: %v", err)
	}

	turn0, err := convs.ListAttachmentsForTurn(context.Background(), "org-1", "thread-1", 0)
	if err != nil {
		t.Fatalf("ListAttachmentsForTurn turn 0: %v", err)
	}
	if len(turn0) != 1 || turn0[0].Filename != "new.txt" || string(turn0[0].Data) != "new" {
		t.Fatalf("turn 0 attachments = %+v", turn0)
	}
	turn1, err := convs.ListAttachmentsForTurn(context.Background(), "org-1", "thread-1", 1)
	if err != nil {
		t.Fatalf("ListAttachmentsForTurn turn 1: %v", err)
	}
	if len(turn1) != 1 || turn1[0].Filename != "follow-up.txt" {
		t.Fatalf("turn 1 attachments = %+v", turn1)
	}
}

func TestAttachmentContentTypesRewriteDangerousMedia(t *testing.T) {
	cases := []string{
		"text/html",
		"text/html; charset=utf-8",
		"application/javascript",
		"text/javascript",
		"image/svg+xml",
	}
	for _, in := range cases {
		if got := normalizeAttachmentContentType(in); got != defaultAttachmentMimeType {
			t.Fatalf("normalizeAttachmentContentType(%q) = %q, want %q", in, got, defaultAttachmentMimeType)
		}
	}
	if got := detectAttachmentContentType("", []byte("<html><script>alert(1)</script></html>")); got != defaultAttachmentMimeType {
		t.Fatalf("detected html content type = %q, want %q", got, defaultAttachmentMimeType)
	}
	if got := normalizeAttachmentContentType("application/json"); got != "application/json" {
		t.Fatalf("json content type = %q, want application/json", got)
	}
}

func TestSafeSandboxFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "screenshot.png", want: "screenshot.png"},
		{in: "../secrets.json", want: "secrets.json"},
		{in: "logs 2026/05.txt", want: "05.txt"},
		{in: "bad\x00name?.json", want: "badname_.json"},
		{in: "  ", want: "attachment"},
	}
	for _, tc := range cases {
		if got := safeSandboxFilename(tc.in); got != tc.want {
			t.Fatalf("safeSandboxFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSafeSandboxSegment(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "", want: "request"},
		{in: " req-123_OK ", want: "req-123_OK"},
		{in: "../", want: "request"},
		{in: "bad/id with spaces", want: "bad_id_with_spaces"},
		{in: strings.Repeat("a", 80), want: strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		if got := safeSandboxSegment(tc.in); got != tc.want {
			t.Fatalf("safeSandboxSegment(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
