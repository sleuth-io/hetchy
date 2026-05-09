package bot

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"

	"github.com/hetchyhq/hetchy/internal/bootstrap"
)

// detectViaSandbox runs bootstrap.Detect against the sandbox's checkout
// by tarring the small, well-known set of files Detect inspects, piping
// them out via shLines, untarring into a host tempdir, and detecting
// there. Why not detect inside the sandbox: Detect's signature takes a
// host filesystem path, and a partial reimplementation (one ReadFile per
// candidate) would silently drift from the real Detect over time.
//
// The candidate set deliberately mirrors detect.go — keep them in sync
// when adding a new hint. languageStats is fed by a sandbox-side find
// that emits relative paths so the host walk treats the tempdir like a
// normal repo root. node_modules / vendor / dist are excluded sandbox-
// side to keep the tar payload bounded.
func (b *Bot) detectViaSandbox(ctx context.Context, sb *daytona.Sandbox, sessionID, workdir string) (*bootstrap.Hints, string, error) {
	tempRoot, err := os.MkdirTemp("", "hetchy-detect-")
	if err != nil {
		return nil, "", fmt.Errorf("detect: tempdir: %w", err)
	}

	// One tar invocation that includes:
	//   - the explicit detection files (best-effort; missing is ok)
	//   - any *.go/.js/.ts/etc. files needed for languageStats
	// We deliberately skip large/irrelevant trees so the tar stays under
	// a few MB on big repos.
	// find -print0 + tar --null pipes the file list through a NUL-
	// delimited stream so paths containing whitespace or shell
	// metacharacters survive intact. The previous `tar $(find …)`
	// pattern was subject to shell word-splitting and silently
	// dropped any file with a space in its name.
	tarCmd := fmt.Sprintf(`cd %s && find . -maxdepth 4 \
      \( -name 'devcontainer.json' \
         -o -name '.devcontainer.json' \
         -o -name 'AGENTS.md' -o -name 'agents.md' \
         -o -name 'docker-compose.yml' -o -name 'docker-compose.yaml' \
         -o -name 'compose.yml' -o -name 'compose.yaml' \
         -o -name 'Dockerfile' -o -name 'Makefile' \
         -o -name 'package.json' -o -name 'go.mod' \
         -o -name '.env.example' -o -name '.env.sample' -o -name '.env.template' \
         -o -name 'README.md' -o -name 'readme.md' -o -name 'README' -o -name 'README.MD' \
         -o -name '*.go' -o -name '*.py' -o -name '*.js' -o -name '*.ts' \
         -o -name '*.tsx' -o -name '*.jsx' -o -name '*.rb' -o -name '*.php' \
         -o -name '*.java' -o -name '*.kt' -o -name '*.rs' \
         -o -name '*.sql' -o -name '*.sh' -o -name '*.yml' -o -name '*.yaml' -o -name '*.toml' \
      \) -type f -not -path '*/node_modules/*' -not -path '*/vendor/*' \
        -not -path '*/.git/*' -not -path '*/dist/*' -not -path '*/build/*' \
        -not -path '*/target/*' -not -path '*/.next/*' \
        -not -path '*/.venv/*' -not -path '*/venv/*' \
        -print0 \
    | tar --null --ignore-failed-read \
        --exclude='./.git' \
        --exclude='./node_modules' \
        --exclude='./vendor' \
        --exclude='./dist' \
        --exclude='./build' \
        --exclude='./target' \
        --exclude='./.next' \
        --exclude='./.venv' \
        --exclude='./venv' \
        -czT - \
    | base64`, shellQuote(workdir))

	out, err := b.shLines(ctx, sb.ID, sb.Process, sessionID, "detect-tar", tarCmd, 90*time.Second, 0, func(string) {})
	if err != nil {
		_ = os.RemoveAll(tempRoot)
		return nil, "", fmt.Errorf("detect: tar via sandbox: %w", err)
	}

	// shLines returns interleaved stdout+stderr — strip non-base64 noise
	// before decoding. base64 output is only [A-Za-z0-9+/=]+ on lines
	// of fixed width; anything else is a stderr trace we can drop.
	var b64 strings.Builder
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.IndexFunc(line, isNotBase64) >= 0 {
			continue
		}
		b64.WriteString(line)
	}
	tarBytes, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		_ = os.RemoveAll(tempRoot)
		return nil, "", fmt.Errorf("detect: decode tar: %w", err)
	}

	// Untar via the host's tar — vastly simpler than reimplementing tar
	// reading and our build deps already include it.
	tarPath := filepath.Join(tempRoot, "repo.tar.gz")
	if err := os.WriteFile(tarPath, tarBytes, 0o644); err != nil {
		_ = os.RemoveAll(tempRoot)
		return nil, "", fmt.Errorf("detect: write tar: %w", err)
	}
	extractDir := filepath.Join(tempRoot, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		_ = os.RemoveAll(tempRoot)
		return nil, "", fmt.Errorf("detect: mkdir extract: %w", err)
	}
	if err := untar(tarPath, extractDir); err != nil {
		_ = os.RemoveAll(tempRoot)
		return nil, "", fmt.Errorf("detect: untar: %w", err)
	}

	hints, err := bootstrap.Detect(extractDir)
	if err != nil {
		_ = os.RemoveAll(tempRoot)
		return nil, "", err
	}
	// Caller is responsible for cleaning up tempRoot once it's done with
	// hints (the path inside hints is meaningless once we delete it, but
	// fingerprinting needs the directory to exist for the duration).
	return hints, tempRoot, nil
}

func isNotBase64(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z':
		return false
	case r >= 'a' && r <= 'z':
		return false
	case r >= '0' && r <= '9':
		return false
	case r == '+' || r == '/' || r == '=':
		return false
	}
	return true
}

// untar extracts a gzip'd tar to dest using the system's tar — relying
// on it avoids pulling archive/tar in just for the bootstrap path, and
// the extracted tree is short-lived (deleted right after Detect returns).
func untar(tarPath, dest string) error {
	cmd := exec.Command("tar", "-xzf", tarPath, "-C", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("untar: %w\n%s", err, string(out))
	}
	return nil
}
