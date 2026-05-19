package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/blocks"
	"github.com/hetchyhq/hetchy/internal/convstore"
)

const (
	maxPromptAttachments      = 5
	maxPromptAttachmentBytes  = 10 * 1024 * 1024
	maxPromptAttachmentTotal  = 25 * 1024 * 1024
	sandboxAttachmentRoot     = "/tmp/hetchy-attachments"
	defaultAttachmentMimeType = "application/octet-stream"
)

type sandboxAttachmentRef struct {
	Filename    string `json:"filename"`
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

func (b *Bot) saveIncomingAttachments(ctx context.Context, orgID, threadID string, turnIndex int, attachments []convstore.Attachment) error {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]convstore.Attachment, 0, len(attachments))
	for _, a := range attachments {
		a.OrgID = orgID
		a.ThreadID = threadID
		a.TurnIndex = turnIndex
		if a.ID == "" {
			a.ID = convstore.NewAttachmentID()
		}
		if a.Filename == "" {
			a.Filename = "attachment"
		}
		if a.ContentType == "" {
			a.ContentType = defaultAttachmentMimeType
		}
		a.ContentType = normalizeAttachmentContentType(a.ContentType)
		if a.Source == "" {
			a.Source = "web"
		}
		a.SizeBytes = int64(len(a.Data))
		out = append(out, a)
	}
	return b.convs.SaveAttachments(ctx, out)
}

func (b *Bot) replaceIncomingAttachmentsForTurn(ctx context.Context, orgID, threadID string, turnIndex int, attachments []convstore.Attachment) error {
	if err := b.convs.DeleteAttachmentsForTurn(ctx, orgID, threadID, turnIndex); err != nil {
		return err
	}
	return b.saveIncomingAttachments(ctx, orgID, threadID, turnIndex, attachments)
}

func (b *Bot) promptWithSandboxAttachments(ctx context.Context, sb *daytona.Sandbox, orgID, threadID string, turnIndex int, requestID, text string, emit blocks.Emitter) (string, error) {
	attachments, err := b.convs.ListAttachmentsForTurn(ctx, orgID, threadID, turnIndex)
	if err != nil {
		return text, err
	}
	refs, err := b.materializePromptAttachments(ctx, sb, requestID, attachments, emit)
	if err != nil {
		return text, err
	}
	return appendAttachmentContext(text, refs), nil
}

func (b *Bot) materializePromptAttachments(ctx context.Context, sb *daytona.Sandbox, requestID string, attachments []convstore.Attachment, emit blocks.Emitter) ([]sandboxAttachmentRef, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	if sb == nil || sb.FileSystem == nil {
		return nil, errors.New("sandbox file system is not available")
	}
	dir := sandboxAttachmentRoot + "/" + safeSandboxSegment(requestID)
	blockID := emit.Start(blocks.KindNotify, "Attachments", nil)
	emit.Append(blockID, fmt.Sprintf("Uploading %d attachment(s) to `%s`.\n", len(attachments), dir))
	if err := sb.FileSystem.CreateFolder(ctx, dir); err != nil {
		emit.Done(blockID, "failed")
		return nil, fmt.Errorf("create attachment directory: %w", err)
	}

	refs := make([]sandboxAttachmentRef, 0, len(attachments))
	for i, a := range attachments {
		if len(a.Data) == 0 && a.SizeBytes > 0 {
			emit.Done(blockID, "failed")
			return nil, fmt.Errorf("attachment %q has no data", a.Filename)
		}
		name := fmt.Sprintf("%02d-%s", i+1, safeSandboxFilename(a.Filename))
		remotePath := dir + "/" + name
		if err := sb.FileSystem.UploadFile(ctx, a.Data, remotePath); err != nil {
			emit.Done(blockID, "failed")
			return nil, fmt.Errorf("upload attachment %q: %w", a.Filename, err)
		}
		refs = append(refs, sandboxAttachmentRef{
			Filename:    a.Filename,
			Path:        remotePath,
			ContentType: a.ContentType,
			SizeBytes:   int64(len(a.Data)),
		})
	}
	manifest, err := json.MarshalIndent(struct {
		Files []sandboxAttachmentRef `json:"files"`
	}{Files: refs}, "", "  ")
	if err == nil {
		if merr := sb.FileSystem.UploadFile(ctx, manifest, dir+"/manifest.json"); merr != nil {
			b.log.Warn("manifest upload failed", "dir", dir, "error", merr)
		}
	}
	emit.Append(blockID, "Attachments are ready for Claude Code.\n")
	emit.Done(blockID, fmt.Sprintf("%d file(s)", len(refs)))
	return refs, nil
}

func appendAttachmentContext(text string, refs []sandboxAttachmentRef) string {
	if len(refs) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(text))
	b.WriteString("\n\nAttached files are available in the sandbox. Use them as context for this request:\n")
	for _, ref := range refs {
		fmt.Fprintf(&b, "- %s (%s, %d bytes): %s\n", ref.Filename, ref.ContentType, ref.SizeBytes, ref.Path)
	}
	b.WriteString("\nA JSON manifest is also available beside them as manifest.json.")
	return b.String()
}

func detectAttachmentContentType(contentType string, data []byte) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" || strings.EqualFold(contentType, defaultAttachmentMimeType) {
		contentType = http.DetectContentType(data)
	}
	return normalizeAttachmentContentType(contentType)
}

func normalizeAttachmentContentType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return defaultAttachmentMimeType
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && isDangerousAttachmentMediaType(mediaType) {
		return defaultAttachmentMimeType
	}
	return contentType
}

func isDangerousAttachmentMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "text/html",
		"image/svg+xml",
		"application/javascript",
		"application/ecmascript",
		"application/x-javascript",
		"text/javascript",
		"text/ecmascript":
		return true
	default:
		return false
	}
}

func safeSandboxSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "request"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "request"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func safeSandboxFilename(name string) string {
	name = strings.TrimSpace(strings.NewReplacer("\\", "/", "\x00", "").Replace(name))
	name = path.Base(name)
	if name == "." || name == "/" || name == "" {
		name = "attachment"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		out = "attachment"
	}
	if len(out) > 120 {
		out = out[:120]
		out = strings.Trim(out, "._-")
		if out == "" {
			out = "attachment"
		}
	}
	return out
}
