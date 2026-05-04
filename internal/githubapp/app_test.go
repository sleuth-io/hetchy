package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func freshPrivateKeyPEM(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return string(pemBytes), key
}

func TestNew_RequiresAllFields(t *testing.T) {
	pemStr, _ := freshPrivateKeyPEM(t)
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "missing app id",
			cfg:  Config{Slug: "x", PrivateKeyPEM: pemStr, WebhookSecret: "s"},
			want: "AppID",
		},
		{
			name: "missing slug",
			cfg:  Config{AppID: 1, PrivateKeyPEM: pemStr, WebhookSecret: "s"},
			want: "Slug",
		},
		{
			name: "missing private key",
			cfg:  Config{AppID: 1, Slug: "x", WebhookSecret: "s"},
			want: "PrivateKeyPEM",
		},
		{
			name: "missing webhook secret",
			cfg:  Config{AppID: 1, Slug: "x", PrivateKeyPEM: pemStr},
			want: "WebhookSecret",
		},
		{
			name: "bad PEM",
			cfg:  Config{AppID: 1, Slug: "x", PrivateKeyPEM: "not a PEM", WebhookSecret: "s"},
			want: "parse private key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg, discardLogger())
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestNew_AcceptsValidConfig(t *testing.T) {
	pemStr, _ := freshPrivateKeyPEM(t)
	app, err := New(Config{
		AppID:         42,
		Slug:          "hetchy",
		PrivateKeyPEM: pemStr,
		WebhookSecret: "s",
	}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if app.AppID() != 42 {
		t.Errorf("AppID = %d, want 42", app.AppID())
	}
	if app.Slug() != "hetchy" {
		t.Errorf("Slug = %q, want hetchy", app.Slug())
	}
}

func TestInstallURL(t *testing.T) {
	pemStr, _ := freshPrivateKeyPEM(t)
	app, err := New(Config{
		AppID: 1, Slug: "hetchy-dev",
		PrivateKeyPEM: pemStr, WebhookSecret: "s",
	}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := app.InstallURL("STATE-TOKEN")
	want := "https://github.com/apps/hetchy-dev/installations/new?state=STATE-TOKEN"
	if got != want {
		t.Errorf("InstallURL = %q, want %q", got, want)
	}
}

// TestAppJWT_VerifiesWithPublicKey confirms the App-level JWT we mint
// to talk to GitHub is signed in a way GitHub can verify: RS256, with
// the matching public key, and the issuer claim equal to the App ID
// as a string.
func TestAppJWT_VerifiesWithPublicKey(t *testing.T) {
	pemStr, key := freshPrivateKeyPEM(t)
	app, err := New(Config{
		AppID: 99, Slug: "hetchy",
		PrivateKeyPEM: pemStr, WebhookSecret: "s",
	}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tok, err := app.appJWT()
	if err != nil {
		t.Fatalf("appJWT: %v", err)
	}
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, jwt.ErrTokenSignatureInvalid
		}
		return &key.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("token reported invalid")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type = %T", parsed.Claims)
	}
	if claims["iss"] != strconv.Itoa(99) {
		t.Errorf("iss = %v, want \"99\"", claims["iss"])
	}
	if _, ok := claims["iat"]; !ok {
		t.Error("missing iat claim")
	}
	if _, ok := claims["exp"]; !ok {
		t.Error("missing exp claim")
	}
}
