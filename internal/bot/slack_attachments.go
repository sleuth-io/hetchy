package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"github.com/hetchyhq/hetchy/internal/convstore"
)

const slackAttachmentDownloadTimeout = 45 * time.Second

var errAttachmentTooLarge = errors.New("attachment too large")

type limitedAttachmentBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedAttachmentBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errAttachmentTooLarge
	}
	return b.Buffer.Write(p)
}

func (b *Bot) downloadSlackAttachments(ctx context.Context, cli *slack.Client, files []slack.File) []convstore.Attachment {
	if cli == nil || len(files) == 0 {
		return nil
	}
	if len(files) > maxPromptAttachments {
		b.log.Warn("slack message has too many attachments; truncating",
			"count", len(files), "max", maxPromptAttachments)
		files = files[:maxPromptAttachments]
	}
	ctx, cancel := context.WithTimeout(ctx, slackAttachmentDownloadTimeout)
	defer cancel()

	out := make([]convstore.Attachment, 0, len(files))
	var total int64
	for _, file := range files {
		if file.Size > maxPromptAttachmentBytes {
			b.log.Warn("slack attachment skipped: too large",
				"file_id", file.ID, "name", file.Name, "size", file.Size)
			continue
		}
		if total+int64(file.Size) > maxPromptAttachmentTotal && file.Size > 0 {
			b.log.Warn("slack attachment skipped: total too large",
				"file_id", file.ID, "name", file.Name, "size", file.Size)
			continue
		}
		attachment, err := b.downloadSlackAttachment(ctx, cli, file)
		if err != nil {
			b.log.Warn("slack attachment download failed",
				"file_id", file.ID, "name", file.Name, "error", err)
			continue
		}
		total += attachment.SizeBytes
		if total > maxPromptAttachmentTotal {
			b.log.Warn("slack attachment skipped after download: total too large",
				"file_id", file.ID, "name", file.Name, "size", attachment.SizeBytes)
			continue
		}
		out = append(out, attachment)
	}
	return out
}

func (b *Bot) downloadSlackAttachment(ctx context.Context, cli *slack.Client, file slack.File) (convstore.Attachment, error) {
	if strings.TrimSpace(file.URLPrivateDownload) == "" && strings.TrimSpace(file.URLPrivate) == "" && strings.TrimSpace(file.ID) != "" {
		info, _, _, err := cli.GetFileInfoContext(ctx, file.ID, 0, 0)
		if err != nil {
			return convstore.Attachment{}, fmt.Errorf("files.info: %w", err)
		}
		if info != nil {
			file = *info
		}
	}
	downloadURL := strings.TrimSpace(file.URLPrivateDownload)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(file.URLPrivate)
	}
	if downloadURL == "" {
		return convstore.Attachment{}, errors.New("no private download URL")
	}
	buf := &limitedAttachmentBuffer{max: maxPromptAttachmentBytes}
	if err := cli.GetFileContext(ctx, downloadURL, buf); err != nil {
		return convstore.Attachment{}, err
	}
	data := append([]byte(nil), buf.Bytes()...)
	name := strings.TrimSpace(file.Name)
	if name == "" {
		name = strings.TrimSpace(file.Title)
	}
	if name == "" {
		name = "slack-attachment"
	}
	contentType := strings.TrimSpace(file.Mimetype)
	if contentType == "" || contentType == defaultAttachmentMimeType {
		contentType = http.DetectContentType(data)
	}
	return convstore.Attachment{
		ID:          convstore.NewAttachmentID(),
		Filename:    name,
		ContentType: contentType,
		SizeBytes:   int64(len(data)),
		Data:        data,
		Source:      "slack",
		SlackFileID: file.ID,
	}, nil
}
