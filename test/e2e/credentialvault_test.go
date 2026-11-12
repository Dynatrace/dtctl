//go:build integration
// +build integration

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/resources/credentialvault"
	"github.com/Dynatrace/dtctl/test/integration"
)

// selfSignedCA returns a throwaway self-signed CA certificate as PEM text.
func selfSignedCA(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestCredentialVaultPublicCertificateLifecycle covers create (flag path and
// -f path, PEM and base64), get, filtered list and delete of extension signing
// CAs, plus an in-place rotation, which exercises the certificate encoding the API requires.
func TestCredentialVaultPublicCertificateLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := credentialvault.NewHandler(env.Client)

	// Flag path: PEM text from a file.
	flagName := env.TestPrefix + "-ca-flags"
	req, err := credentialvault.BuildRequest(flagName, "PUBLIC_CERTIFICATE", "dtctl e2e", []string{"EXTENSION"}, false,
		credentialvault.SecretInput{Certificate: selfSignedCA(t, flagName)})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	flagID, err := handler.Create(req)
	if err != nil {
		t.Fatalf("create (flag path): %v", err)
	}
	env.Cleanup.Track("credential", flagID, flagName)
	t.Logf("✓ Created %s (%s)", flagName, flagID)

	// -f path: the definition holds base64 (the API field format).
	fileName := env.TestPrefix + "-ca-file"
	b64 := base64.StdEncoding.EncodeToString(selfSignedCA(t, fileName))
	def := credentialvault.CredentialRequest{Name: fileName, Type: "public-certificate", Scopes: []string{"extension"}, Certificate: &b64}
	if err := credentialvault.NormalizeRequest(&def); err != nil {
		t.Fatalf("NormalizeRequest: %v", err)
	}
	fileID, err := handler.Create(def)
	if err != nil {
		t.Fatalf("create (-f path, base64): %v", err)
	}
	env.Cleanup.Track("credential", fileID, fileName)
	t.Logf("✓ Created %s (%s)", fileName, fileID)

	got, err := handler.Get(flagID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != flagName || got.Type != "PUBLIC_CERTIFICATE" || len(got.Scopes) != 1 || got.Scopes[0] != "EXTENSION" {
		t.Errorf("unexpected credential: %+v", got)
	}

	// Rotate the certificate in place: the ID must survive.
	rot, err := credentialvault.BuildRequest(flagName+"-rotated", "PUBLIC_CERTIFICATE", "dtctl e2e", []string{"EXTENSION"}, false,
		credentialvault.SecretInput{Certificate: selfSignedCA(t, flagName+"-rotated")})
	if err != nil {
		t.Fatalf("BuildRequest (rotate): %v", err)
	}
	if err := handler.Update(flagID, rot); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, err := handler.Get(flagID); err != nil || got.Name != flagName+"-rotated" {
		t.Errorf("after update: %+v, %v", got, err)
	}
	t.Logf("✓ Rotated %s in place", flagID)

	// The type filter for PUBLIC_CERTIFICATE runs client-side.
	list, err := handler.List(credentialvault.ListOptions{Type: "PUBLIC_CERTIFICATE", Scope: "EXTENSION", PageSize: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range list {
		if c.Type != "PUBLIC_CERTIFICATE" {
			t.Errorf("type filter leaked %s (%s)", c.ID, c.Type)
		}
		seen[c.ID] = true
	}
	if !seen[flagID] || !seen[fileID] {
		t.Errorf("created credentials missing from the paginated, filtered list")
	}

	for _, id := range []string{flagID, fileID} {
		if err := handler.Delete(id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
		if _, err := handler.Get(id); err == nil {
			t.Errorf("credential %s still exists after delete", id)
		}
	}
	t.Log("✓ Deleted both credentials")
}
