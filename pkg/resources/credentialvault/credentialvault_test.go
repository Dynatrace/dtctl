package credentialvault

import (
	"encoding/base64"
	"strings"
	"testing"

	sdkcv "github.com/Dynatrace/dtctl/sdk/api/credentialvault"
)

const testPEM = "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUZmFrZQ==\n-----END CERTIFICATE-----\n"

func TestNormalizeType(t *testing.T) {
	for in, want := range map[string]string{
		"public-certificate": "PUBLIC_CERTIFICATE",
		"PUBLIC_CERTIFICATE": "PUBLIC_CERTIFICATE",
		"Username-Password":  "USERNAME_PASSWORD",
		"token":              "TOKEN",
	} {
		got, err := NormalizeType(in)
		if err != nil || got != want {
			t.Errorf("NormalizeType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeType("bogus"); err == nil || !strings.Contains(err.Error(), "public-certificate") {
		t.Errorf("want error listing valid types, got %v", err)
	}
}

func TestNormalizeScopesDedupes(t *testing.T) {
	got, err := NormalizeScopes([]string{"extension-authentication", "EXTENSION_AUTHENTICATION", "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "EXTENSION_AUTHENTICATION,SYNTHETIC" {
		t.Errorf("got %v", got)
	}
	if _, err := NormalizeScopes([]string{"nope"}); err == nil {
		t.Error("want error for unknown scope")
	}
}

func TestBuildRequestPublicCertificate(t *testing.T) {
	req, err := BuildRequest("ca", sdkcv.TypePublicCertificate, "", []string{sdkcv.ScopeExtensionAuthentication}, false,
		SecretInput{Certificate: []byte(testPEM)})
	if err != nil {
		t.Fatal(err)
	}
	if req.Certificate == nil || *req.Certificate != base64.StdEncoding.EncodeToString([]byte(testPEM)) {
		t.Error("public certificate must be sent as base64-encoded PEM")
	}
	if req.CertificateFormat == nil || *req.CertificateFormat != "PEM" {
		t.Error("certificateFormat must be PEM")
	}
	if req.Password == nil || *req.Password != "" {
		t.Error("password must be present and empty")
	}
}

func TestBuildRequestPublicCertificateRejectsPrivateKey(t *testing.T) {
	key := "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"
	_, err := BuildRequest("ca", sdkcv.TypePublicCertificate, "", []string{sdkcv.ScopeExtensionAuthentication}, false,
		SecretInput{Certificate: []byte(testPEM + key)})
	if err == nil || !strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("want private-key rejection, got %v", err)
	}
	_, err = BuildRequest("ca", sdkcv.TypePublicCertificate, "", []string{sdkcv.ScopeExtensionAuthentication}, false,
		SecretInput{Certificate: []byte("not a pem")})
	if err == nil {
		t.Fatal("want error for non-PEM input")
	}
}

func TestBuildRequestCertificatePKCS12(t *testing.T) {
	raw := []byte{0x30, 0x82, 0x01, 0x02}
	req, err := BuildRequest("client", sdkcv.TypeCertificate, "", []string{sdkcv.ScopeExtension}, true,
		SecretInput{Certificate: raw, Password: []byte("pw\n")})
	if err != nil {
		t.Fatal(err)
	}
	if *req.CertificateFormat != "PKCS12" {
		t.Errorf("format = %s, want detected PKCS12", *req.CertificateFormat)
	}
	if *req.Certificate != base64.StdEncoding.EncodeToString(raw) {
		t.Error("certificate must be base64 file bytes")
	}
	if *req.Password != base64.StdEncoding.EncodeToString([]byte("pw")) {
		t.Error("password must be base64 with trailing newline trimmed")
	}
	if !req.OwnerAccessOnly {
		t.Error("ownerAccessOnly not propagated")
	}
}

func TestBuildRequestCertificatePEMRejectsPassword(t *testing.T) {
	_, err := BuildRequest("client", sdkcv.TypeCertificate, "", []string{sdkcv.ScopeExtension}, false,
		SecretInput{Certificate: []byte(testPEM), Password: []byte("pw")})
	if err == nil {
		t.Fatal("want error: PEM certificates take no password")
	}
}

func TestBuildRequestTokenAndUserPassword(t *testing.T) {
	req, err := BuildRequest("t", sdkcv.TypeToken, "", []string{sdkcv.ScopeExtension}, false, SecretInput{Token: []byte("abc\n")})
	if err != nil || *req.Token != "abc" {
		t.Fatalf("token: %v %v", req.Token, err)
	}
	if _, err := BuildRequest("u", sdkcv.TypeUsernamePassword, "", []string{sdkcv.ScopeSynthetic}, false,
		SecretInput{User: "a:b", Password: []byte("x")}); err == nil {
		t.Error("want error for ':' in user")
	}
	req, err = BuildRequest("u", sdkcv.TypeUsernamePassword, "", []string{sdkcv.ScopeSynthetic}, false,
		SecretInput{User: "svc", Password: []byte("x\r\n")})
	if err != nil || *req.User != "svc" || *req.Password != "x" {
		t.Fatalf("username-password: %+v %v", req, err)
	}
}

func TestBuildRequestRequiresNameScopeAndSecrets(t *testing.T) {
	cases := map[string]struct {
		name   string
		typ    string
		scopes []string
		in     SecretInput
	}{
		"no name":   {"", sdkcv.TypeToken, []string{sdkcv.ScopeExtension}, SecretInput{Token: []byte("x")}},
		"no scope":  {"n", sdkcv.TypeToken, nil, SecretInput{Token: []byte("x")}},
		"no token":  {"n", sdkcv.TypeToken, []string{sdkcv.ScopeExtension}, SecretInput{}},
		"no cert":   {"n", sdkcv.TypePublicCertificate, []string{sdkcv.ScopeExtension}, SecretInput{}},
		"aws flags": {"n", sdkcv.TypeAWSKeyBased, []string{sdkcv.ScopeExtension}, SecretInput{}},
	}
	for label, c := range cases {
		if _, err := BuildRequest(c.name, c.typ, "", c.scopes, false, c.in); err == nil {
			t.Errorf("%s: want error", label)
		}
	}
}

func TestNormalizeRequest(t *testing.T) {
	req := CredentialRequest{Name: "n", Type: "token", Scopes: []string{"extension-authentication"}}
	if err := NormalizeRequest(&req); err != nil {
		t.Fatal(err)
	}
	if req.Type != "TOKEN" || req.Scopes[0] != "EXTENSION_AUTHENTICATION" {
		t.Errorf("got %+v", req)
	}
	if err := NormalizeRequest(&CredentialRequest{Name: "n", Type: "TOKEN"}); err == nil {
		t.Error("want error for missing scopes")
	}
}

func TestNormalizeRequestPublicCertificateDefaults(t *testing.T) {
	cert := testPEM
	req := CredentialRequest{Name: "n", Type: "public-certificate", Scopes: []string{"extension"}, Certificate: &cert}
	if err := NormalizeRequest(&req); err != nil {
		t.Fatal(err)
	}
	if req.Password == nil || *req.Password != "" || req.CertificateFormat == nil || *req.CertificateFormat != "PEM" {
		t.Errorf("want password \"\" and format PEM, got %+v", req)
	}
	if req.Certificate == nil || *req.Certificate != base64.StdEncoding.EncodeToString([]byte(testPEM)) {
		t.Error("the -f path must send the certificate base64-encoded")
	}
	bad := "not a pem"
	if err := NormalizeRequest(&CredentialRequest{Name: "n", Type: "PUBLIC_CERTIFICATE", Scopes: []string{"EXTENSION"}, Certificate: &bad}); err == nil {
		t.Error("want PEM validation on the -f path")
	}
	if err := NormalizeRequest(&CredentialRequest{Name: "n", Type: "PUBLIC_CERTIFICATE", Scopes: []string{"EXTENSION"}}); err == nil {
		t.Error("want error for missing certificate")
	}
}

func TestFromSDKFoldsDeprecatedScope(t *testing.T) {
	c := fromSDK(&sdkcv.Credential{ID: "x", Scope: "SYNTHETIC"})
	if len(c.Scopes) != 1 || c.Scopes[0] != "SYNTHETIC" || c.ScopeList != "SYNTHETIC" {
		t.Errorf("got %+v", c)
	}
}

// Every CLI spelling a map lists, and its API enum, must be accepted: the
// valid list in the error message is built from these keys.
func TestEveryListedSpellingNormalizes(t *testing.T) {
	for _, tc := range []struct {
		names map[string]string
		norm  func(string) (string, error)
	}{{typeNames, NormalizeType}, {scopeNames, NormalizeScope}} {
		for cli, api := range tc.names {
			for _, in := range []string{cli, strings.ToUpper(cli), api, " " + cli + " "} {
				if got, err := tc.norm(in); err != nil || got != api {
					t.Errorf("normalize(%q) = %q, %v; want %q", in, got, err, api)
				}
			}
		}
	}
}

func TestNormalizeRequestAcceptsPEMOrBase64Certificates(t *testing.T) {
	b64PEM := base64.StdEncoding.EncodeToString([]byte(testPEM))
	pkcs12 := []byte{0x30, 0x82, 0x01, 0x02, 0xde, 0xad}
	b64P12 := base64.StdEncoding.EncodeToString(pkcs12)
	for _, tc := range []struct {
		name, typ, cert, wantFormat string
		wantRaw                     []byte
	}{
		{"public PEM text", "PUBLIC_CERTIFICATE", testPEM, "PEM", []byte(testPEM)},
		{"public base64 PEM", "PUBLIC_CERTIFICATE", b64PEM, "PEM", []byte(testPEM)},
		{"certificate PEM text", "CERTIFICATE", testPEM, "PEM", []byte(testPEM)},
		{"certificate base64 PEM", "CERTIFICATE", b64PEM, "PEM", []byte(testPEM)},
		{"certificate base64 PKCS12", "CERTIFICATE", b64P12, "PKCS12", pkcs12},
	} {
		cert := tc.cert
		req := CredentialRequest{Name: "n", Type: tc.typ, Scopes: []string{"extension"}, Certificate: &cert}
		if err := NormalizeRequest(&req); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if *req.Certificate != base64.StdEncoding.EncodeToString(tc.wantRaw) {
			t.Errorf("%s: certificate not sent as base64 of the original bytes", tc.name)
		}
		if req.CertificateFormat == nil || *req.CertificateFormat != tc.wantFormat {
			t.Errorf("%s: format = %v, want %s", tc.name, req.CertificateFormat, tc.wantFormat)
		}
	}
}

func TestNormalizeRequestRejectsMismatchedCertificateFormat(t *testing.T) {
	p12 := "PKCS12"
	pemFmt := "PEM"
	b64P12 := base64.StdEncoding.EncodeToString([]byte{0x30, 0x82, 0x01})
	for name, req := range map[string]CredentialRequest{
		"public PKCS12":        {Type: "PUBLIC_CERTIFICATE", Certificate: ptr(testPEM), CertificateFormat: &p12},
		"certificate PEM bin":  {Type: "CERTIFICATE", Certificate: &b64P12, CertificateFormat: &pemFmt},
		"certificate P12 text": {Type: "CERTIFICATE", Certificate: ptr(testPEM), CertificateFormat: &p12},
		"neither PEM nor b64":  {Type: "CERTIFICATE", Certificate: ptr("not a cert!")},
	} {
		req.Name, req.Scopes = "n", []string{"extension"}
		if err := NormalizeRequest(&req); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func ptr(s string) *string { return &s }
