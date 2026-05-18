package bot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestLimitedAttachmentBufferRejectsOversizeWrites(t *testing.T) {
	buf := &limitedAttachmentBuffer{max: 4}
	if n, err := buf.Write([]byte("abcd")); err != nil || n != 4 {
		t.Fatalf("first write n=%d err=%v, want 4 nil", n, err)
	}
	if n, err := buf.Write([]byte("e")); !errors.Is(err, errAttachmentTooLarge) || n != 0 {
		t.Fatalf("oversize write n=%d err=%v, want 0 errAttachmentTooLarge", n, err)
	}
	if got := buf.String(); got != "abcd" {
		t.Fatalf("buffer = %q, want abcd", got)
	}
}

func TestDownloadSlackAttachmentsFetchesUsableFiles(t *testing.T) {
	const payload = "slack file context"
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q, want Bearer test-token", got)
		}
		if r.URL.Path != "/download" {
			t.Fatalf("path = %q, want /download", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	b := &Bot{log: discardLogger()}
	cli := slack.New("test-token")
	got := b.downloadSlackAttachments(context.Background(), cli, []slack.File{
		{
			ID:                 "too-big",
			Name:               "too-big.txt",
			Size:               maxPromptAttachmentBytes + 1,
			URLPrivateDownload: server.URL + "/too-big",
		},
		{
			ID:                 "file-1",
			Title:              "fallback-name.txt",
			Mimetype:           "text/plain",
			Size:               len(payload),
			URLPrivateDownload: server.URL + "/download",
		},
		{
			Name: "missing-url.txt",
			Size: 1,
		},
	})

	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(got), got)
	}
	a := got[0]
	if a.Filename != "fallback-name.txt" || a.ContentType != "text/plain" || a.Source != "slack" {
		t.Fatalf("metadata = %+v", a)
	}
	if a.SlackFileID != "file-1" || a.SizeBytes != int64(len(payload)) || string(a.Data) != payload {
		t.Fatalf("downloaded attachment = %+v data=%q", a, string(a.Data))
	}
	if strings.TrimSpace(a.ID) == "" {
		t.Fatal("expected generated attachment id")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}
