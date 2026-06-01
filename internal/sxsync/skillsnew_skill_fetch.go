package sxsync

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Masterminds/semver/v3"
	sxlib "github.com/sleuth-io/sx/pkg/sxvault"
)

// skillsNewAssetMetadata is the subset of metadata.toml we surface to the UI
// when rendering a skill modal. Skills.new's asset zips always include a
// top-level metadata.toml whose [asset] section carries these fields.
type skillsNewAssetMetadata struct {
	Asset struct {
		Name        string `toml:"name"`
		Version     string `toml:"version"`
		Type        string `toml:"type"`
		Description string `toml:"description"`
	} `toml:"asset"`
}

// fetchSkillsNewSkillZip downloads a skill asset zip directly from skills.new
// and extracts its metadata. We bypass sxlib.GetAssetZip on this read path
// because skills.new's /api/skills/assets/{name}/{version}/metadata.toml
// endpoint currently returns the full asset zip blob (content-type:
// application/zip) instead of the metadata.toml inner file — sxlib then tries
// to TOML-parse those zip bytes and fails with "files cannot contain NULL
// bytes", which surfaced to skill-modal users as HTTP 502 in production. Until
// the skills.new server fixes that endpoint we fetch the zip directly here and
// parse metadata.toml out of the zip ourselves.
func fetchSkillsNewSkillZip(ctx context.Context, httpClient *http.Client, serverURL, authToken, name string) (AssetZip, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AssetZip{}, errors.New("sxsync: skill name is required")
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		serverURL = sxlib.DefaultSkillsNewURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	encodedName := url.PathEscape(name)

	versions, err := listSkillsNewSkillVersions(ctx, httpClient, serverURL, authToken, encodedName, name)
	if err != nil {
		return AssetZip{}, err
	}
	if len(versions) == 0 {
		// Skills.new returns HTTP 200 with an empty body for unknown asset
		// names rather than 404 (the catch-all SPA route happens further up
		// the stack); empty version list is the canonical "asset not in
		// this vault" signal.
		return AssetZip{}, fmt.Errorf("sxvault: asset %q not found in vault", name)
	}
	version := pickHighestSkillsNewVersion(versions)
	encodedVer := url.PathEscape(version)

	zipURL := fmt.Sprintf("%s/api/skills/assets/%s/%s/%s-%s.zip", serverURL, encodedName, encodedVer, encodedName, encodedVer)
	body, contentType, err := skillsNewGetAuthorized(ctx, httpClient, zipURL, authToken)
	if err != nil {
		return AssetZip{}, fmt.Errorf("sxvault: reading asset zip for %q@%s: %w", name, version, err)
	}
	if !looksLikeZipPayload(body, contentType) {
		// Same Nuxt-SPA-catch-all behavior as above: a missing asset comes
		// back as HTTP 200 with HTML, not 404. Treat that as missing so the
		// caller can fall back to the public vault.
		return AssetZip{}, fmt.Errorf("sxvault: asset %q@%s not found in vault", name, version)
	}

	meta, err := parseSkillsNewMetadataFromZip(body)
	if err != nil {
		return AssetZip{}, fmt.Errorf("sxvault: parsing metadata for %q@%s: %w", name, version, err)
	}
	canonical := strings.TrimSpace(meta.Asset.Name)
	if canonical == "" {
		canonical = name
	}
	return AssetZip{
		Name:        canonical,
		Version:     version,
		Type:        meta.Asset.Type,
		Description: meta.Asset.Description,
		Data:        body,
	}, nil
}

func listSkillsNewSkillVersions(ctx context.Context, httpClient *http.Client, serverURL, authToken, encodedName, displayName string) ([]string, error) {
	listURL := fmt.Sprintf("%s/api/skills/assets/%s/list.txt", serverURL, encodedName)
	body, _, err := skillsNewGetAuthorized(ctx, httpClient, listURL, authToken)
	if err != nil {
		return nil, fmt.Errorf("sxvault: listing versions for %q: %w", displayName, err)
	}
	return parseSkillsNewVersionList(body), nil
}

func parseSkillsNewVersionList(body []byte) []string {
	out := make([]string, 0)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		v := strings.TrimSpace(scanner.Text())
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

// pickHighestSkillsNewVersion mirrors sxlib's highestSemver: take the
// greatest-semver entry when any parse, otherwise fall back to the last entry
// (the convention sx CLI itself uses for non-semver vaults).
func pickHighestSkillsNewVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	best := versions[len(versions)-1]
	var bestParsed *semver.Version
	for _, v := range versions {
		parsed, err := semver.NewVersion(v)
		if err != nil {
			continue
		}
		if bestParsed == nil || parsed.GreaterThan(bestParsed) {
			bestParsed = parsed
			best = v
		}
	}
	return best
}

func skillsNewGetAuthorized(ctx context.Context, httpClient *http.Client, urlStr, authToken string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, "", err
	}
	if t := strings.TrimSpace(authToken); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	req.Header.Set("Accept", "*/*")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.Header.Get("Content-Type"), fmt.Errorf("read response body: %w", readErr)
	}
	if resp.StatusCode == http.StatusNotFound {
		// Include the request path (no query, no auth header) so an
		// auth-shape regression doesn't disappear into an opaque message.
		// req.URL is the resolved URL; its Path is enough context for a
		// debugger and never carries the bearer token.
		return nil, resp.Header.Get("Content-Type"), fmt.Errorf("asset not found: HTTP 404 for %s", req.URL.Path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header.Get("Content-Type"), fmt.Errorf("HTTP %d for %s: %s", resp.StatusCode, req.URL.Path, strings.TrimSpace(string(body)))
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// looksLikeZipPayload tells a real asset-zip response apart from the
// Nuxt-SPA HTML that skills.new serves for unknown paths. Both come back as
// HTTP 200, so we have to inspect the body: zip magic "PK\x03\x04" or an
// explicit application/zip content type are the only reliable signals.
func looksLikeZipPayload(body []byte, contentType string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "application/zip") {
		return true
	}
	return len(body) >= 4 && body[0] == 'P' && body[1] == 'K' && body[2] == 0x03 && body[3] == 0x04
}

func parseSkillsNewMetadataFromZip(zipBytes []byte) (skillsNewAssetMetadata, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return skillsNewAssetMetadata{}, fmt.Errorf("open skill zip: %w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.EqualFold(f.Name, "metadata.toml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return skillsNewAssetMetadata{}, fmt.Errorf("open metadata.toml in skill zip: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		_ = rc.Close()
		if err != nil {
			return skillsNewAssetMetadata{}, fmt.Errorf("read metadata.toml: %w", err)
		}
		var meta skillsNewAssetMetadata
		if err := toml.Unmarshal(data, &meta); err != nil {
			return skillsNewAssetMetadata{}, fmt.Errorf("parse metadata.toml: %w", err)
		}
		return meta, nil
	}
	// A skill zip without metadata.toml is not actionable, but we keep the
	// download going by treating it as a missing-asset signal so the caller
	// can fall back to the public vault instead of bubbling up a 5xx.
	return skillsNewAssetMetadata{}, errors.New("metadata.toml not found in skill zip")
}
