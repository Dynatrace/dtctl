// Package credentialvault is the CLI resource handler for the Dynatrace
// Credential vault. It delegates HTTP calls to sdk/api/credentialvault and adds
// display fields and input normalization.
package credentialvault

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcv "github.com/Dynatrace/dtctl/sdk/api/credentialvault"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Re-exported SDK types and constants.
type (
	AccessData        = sdkcv.AccessData
	CredentialRequest = sdkcv.CredentialRequest
	ListOptions       = sdkcv.ListOptions
)

// Credential is the CLI read model for a credentials set. It carries metadata
// only; the API never returns secret material.
type Credential struct {
	ID                       string           `json:"id" table:"ID"`
	Name                     string           `json:"name" table:"NAME"`
	Type                     string           `json:"type" table:"TYPE"`
	Scopes                   []string         `json:"scopes,omitempty" table:"-"`
	ScopeList                string           `json:"-" yaml:"-" table:"SCOPES"`
	Owner                    string           `json:"owner,omitempty" table:"OWNER,wide"`
	OwnerAccessOnly          bool             `json:"ownerAccessOnly" table:"OWNER_ONLY,wide"`
	Description              string           `json:"description,omitempty" table:"DESCRIPTION,wide"`
	AllowContextlessRequests bool             `json:"allowContextlessRequests,omitempty" table:"-"`
	AllowedEntities          []AccessData     `json:"allowedEntities,omitempty" table:"-"`
	CredentialUsageSummary   []map[string]any `json:"credentialUsageSummary,omitempty" table:"-"`
}

// fromSDK converts an SDK credential, folding the deprecated singular `scope`
// into Scopes when the API returns only that.
func fromSDK(c *sdkcv.Credential) *Credential {
	scopes := c.Scopes
	if len(scopes) == 0 && c.Scope != "" {
		scopes = []string{c.Scope}
	}
	return &Credential{
		ID:                       c.ID,
		Name:                     c.Name,
		Type:                     c.Type,
		Scopes:                   scopes,
		ScopeList:                strings.Join(scopes, ","),
		Owner:                    c.Owner,
		OwnerAccessOnly:          c.OwnerAccessOnly,
		Description:              c.Description,
		AllowContextlessRequests: c.AllowContextlessRequests,
		AllowedEntities:          c.AllowedEntities,
		CredentialUsageSummary:   c.CredentialUsageSummary,
	}
}

// Handler handles Credential vault resources.
type Handler struct {
	sdk *sdkcv.Handler
}

// NewHandler creates a new Credential vault handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{sdk: sdkcv.NewHandler(httpclient.Wrap(c.HTTP()))}
}

// List lists credentials (all pages).
func (h *Handler) List(opts ListOptions) ([]Credential, error) {
	l, err := h.sdk.List(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	out := make([]Credential, len(l.Credentials))
	for i := range l.Credentials {
		out[i] = *fromSDK(&l.Credentials[i])
	}
	return out, nil
}

// Get gets the metadata of a credentials set.
func (h *Handler) Get(id string) (*Credential, error) {
	c, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return fromSDK(c), nil
}

// Create creates a credentials set and returns its ID.
func (h *Handler) Create(req CredentialRequest) (string, error) {
	return h.sdk.Create(context.Background(), req)
}

// Update replaces a credentials set, keeping its ID. The request must carry
// the secret material again.
func (h *Handler) Update(id string, req CredentialRequest) error {
	return h.sdk.Update(context.Background(), id, req)
}

// Delete deletes a credentials set.
func (h *Handler) Delete(id string) error {
	return h.sdk.Delete(context.Background(), id)
}

// typeNames maps the CLI spelling (kebab-case) to the API enum. Every API
// value is also accepted verbatim, in any case.
var typeNames = map[string]string{
	"public-certificate": sdkcv.TypePublicCertificate,
	"certificate":        sdkcv.TypeCertificate,
	"token":              sdkcv.TypeToken,
	"username-password":  sdkcv.TypeUsernamePassword,
	"aws-key-based":      sdkcv.TypeAWSKeyBased,
	"aws-role-based":     sdkcv.TypeAWSRoleBased,
	"snmpv3":             sdkcv.TypeSNMPV3,
}

var scopeNames = map[string]string{
	"app-engine":               sdkcv.ScopeAppEngine,
	"extension":                sdkcv.ScopeExtension,
	"extension-authentication": sdkcv.ScopeExtensionAuthentication,
	"synthetic":                sdkcv.ScopeSynthetic,
}

// toEnum turns "public-certificate" / "PUBLIC_CERTIFICATE" / "Public_Certificate"
// into "PUBLIC_CERTIFICATE".
func toEnum(v string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(v), "-", "_"))
}

func lookupEnum(kind, v string, names map[string]string) (string, error) {
	// The CLI spelling first: it does not always derive from the enum
	// (aws-key-based is AWS_MONITORING_KEY_BASED).
	if api, ok := names[strings.ToLower(strings.TrimSpace(v))]; ok {
		return api, nil
	}
	want := toEnum(v)
	for _, api := range names {
		if api == want {
			return api, nil
		}
	}
	valid := make([]string, 0, len(names))
	for k := range names {
		valid = append(valid, k)
	}
	sort.Strings(valid)
	return "", fmt.Errorf("unknown credential %s %q (valid: %s)", kind, v, strings.Join(valid, ", "))
}

// NormalizeType converts a CLI or API spelling of a credential type to the API
// enum, rejecting unknown values.
func NormalizeType(v string) (string, error) { return lookupEnum("type", v, typeNames) }

// NormalizeScope converts a CLI or API spelling of a credential scope to the
// API enum, rejecting unknown values.
func NormalizeScope(v string) (string, error) { return lookupEnum("scope", v, scopeNames) }

// NormalizeScopes normalizes a list of scopes, dropping duplicates.
func NormalizeScopes(vs []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		s, err := NormalizeScope(v)
		if err != nil {
			return nil, err
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// SecretInput carries the secret material for BuildRequest. Byte slices come
// from files or stdin, never from flags.
type SecretInput struct {
	Certificate       []byte // PEM text, or PKCS#12 bytes for CERTIFICATE
	CertificateFormat string // "", "pem" or "pkcs12"; "" means detect
	Password          []byte // CERTIFICATE (PKCS#12) or USERNAME_PASSWORD
	Token             []byte // TOKEN
	User              string // USERNAME_PASSWORD
}

// BuildRequest assembles a create request for the flag-driven path. typ and
// scopes must already be normalized.
func BuildRequest(name, typ, description string, scopes []string, ownerAccessOnly bool, in SecretInput) (CredentialRequest, error) {
	req := CredentialRequest{
		Name:            name,
		Type:            typ,
		Description:     description,
		OwnerAccessOnly: ownerAccessOnly,
		Scopes:          scopes,
	}
	if name == "" {
		return req, fmt.Errorf("--name is required")
	}
	if len(scopes) == 0 {
		return req, fmt.Errorf("at least one --scope is required")
	}

	switch typ {
	case sdkcv.TypePublicCertificate:
		if len(in.Certificate) == 0 {
			return req, fmt.Errorf("--certificate-file is required for type public-certificate")
		}
		if f := toEnum(in.CertificateFormat); f != "" && f != sdkcv.CertificateFormatPEM {
			return req, fmt.Errorf("type public-certificate supports only PEM certificates")
		}
		if err := validatePEMCertificate(in.Certificate); err != nil {
			return req, err
		}
		// The Credential Vault API decodes `certificate` as base64 for every
		// certificate type; raw PEM text is rejected.
		cert := base64.StdEncoding.EncodeToString(in.Certificate)
		format := sdkcv.CertificateFormatPEM
		empty := "" // the API requires the field; a password is not supported
		req.Certificate, req.CertificateFormat, req.Password = &cert, &format, &empty

	case sdkcv.TypeCertificate:
		if len(in.Certificate) == 0 {
			return req, fmt.Errorf("--certificate-file is required for type certificate")
		}
		format := toEnum(in.CertificateFormat)
		if format == "" {
			format = sdkcv.CertificateFormatPKCS12
			if blk, _ := pem.Decode(in.Certificate); blk != nil {
				format = sdkcv.CertificateFormatPEM
			}
		}
		if format != sdkcv.CertificateFormatPEM && format != sdkcv.CertificateFormatPKCS12 {
			return req, fmt.Errorf("unknown certificate format %q (valid: pem, pkcs12)", in.CertificateFormat)
		}
		cert := base64.StdEncoding.EncodeToString(in.Certificate)
		pw := ""
		if format == sdkcv.CertificateFormatPEM {
			if len(in.Password) > 0 {
				return req, fmt.Errorf("a password is not supported for PEM certificates")
			}
		} else {
			pw = base64.StdEncoding.EncodeToString(trimNewline(in.Password))
		}
		req.Certificate, req.CertificateFormat, req.Password = &cert, &format, &pw

	case sdkcv.TypeToken:
		tok := string(trimNewline(in.Token))
		if tok == "" {
			return req, fmt.Errorf("--token-file is required for type token")
		}
		req.Token = &tok

	case sdkcv.TypeUsernamePassword:
		if in.User == "" {
			return req, fmt.Errorf("--user is required for type username-password")
		}
		if strings.Contains(in.User, ":") {
			return req, fmt.Errorf("--user must not contain ':'")
		}
		pw := string(trimNewline(in.Password))
		if pw == "" {
			return req, fmt.Errorf("--password-file is required for type username-password")
		}
		user := in.User
		req.User, req.Password = &user, &pw

	default:
		return req, fmt.Errorf("type %s cannot be created from flags; use -f with a full definition", typ)
	}
	return req, nil
}

// NormalizeRequest normalizes type and scopes of a request read from a file.
func NormalizeRequest(req *CredentialRequest) error {
	if req.Name == "" {
		return fmt.Errorf("definition is missing `name`")
	}
	t, err := NormalizeType(req.Type)
	if err != nil {
		return err
	}
	req.Type = t
	scopes, err := NormalizeScopes(req.Scopes)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return fmt.Errorf("definition is missing `scopes`")
	}
	req.Scopes = scopes

	// Match the flag path. The certificate may be written as PEM text or as
	// base64 (the API field format); either way it is sent base64-encoded. The
	// API marks `password` required on certificate types (empty for PEM and
	// public certificates).
	switch req.Type {
	case sdkcv.TypePublicCertificate:
		raw, err := certificateFromDefinition(req.Certificate)
		if err != nil {
			return err
		}
		if req.CertificateFormat != nil && toEnum(*req.CertificateFormat) != sdkcv.CertificateFormatPEM {
			return fmt.Errorf("type PUBLIC_CERTIFICATE supports only certificateFormat PEM")
		}
		if err := validatePEMCertificate(raw); err != nil {
			return err
		}
		enc := base64.StdEncoding.EncodeToString(raw)
		f := sdkcv.CertificateFormatPEM
		req.Certificate, req.CertificateFormat = &enc, &f
		if req.Password == nil {
			empty := ""
			req.Password = &empty
		}
	case sdkcv.TypeCertificate:
		raw, err := certificateFromDefinition(req.Certificate)
		if err != nil {
			return err
		}
		isPEM := false
		if blk, _ := pem.Decode(raw); blk != nil {
			isPEM = true
		}
		format := sdkcv.CertificateFormatPKCS12
		if isPEM {
			format = sdkcv.CertificateFormatPEM
		}
		if req.CertificateFormat != nil && *req.CertificateFormat != "" {
			format = toEnum(*req.CertificateFormat)
		}
		switch {
		case format != sdkcv.CertificateFormatPEM && format != sdkcv.CertificateFormatPKCS12:
			return fmt.Errorf("unknown certificateFormat %q (valid: PEM, PKCS12)", format)
		case format == sdkcv.CertificateFormatPEM && !isPEM:
			return fmt.Errorf("certificateFormat is PEM but `certificate` is not PEM text or base64-encoded PEM")
		case format == sdkcv.CertificateFormatPKCS12 && isPEM:
			return fmt.Errorf("certificateFormat is PKCS12 but `certificate` holds PEM text")
		}
		enc := base64.StdEncoding.EncodeToString(raw)
		req.Certificate, req.CertificateFormat = &enc, &format
		if req.Password == nil {
			empty := ""
			req.Password = &empty
		}
	}
	return nil
}

// certificateFromDefinition returns the certificate bytes of a definition's
// `certificate` field, which may hold PEM text or base64 (of PEM text or of a
// PKCS#12 file).
func certificateFromDefinition(v *string) ([]byte, error) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil, fmt.Errorf("definition is missing `certificate`")
	}
	if blk, _ := pem.Decode([]byte(*v)); blk != nil {
		return []byte(*v), nil
	}
	compact := strings.Join(strings.Fields(*v), "")
	raw, err := base64.StdEncoding.DecodeString(compact)
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("`certificate` must be PEM text or base64")
	}
	return raw, nil
}

func validatePEMCertificate(b []byte) error {
	rest := b
	found := false
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			return fmt.Errorf("certificate file contains a %q PEM block; a public certificate must contain only CERTIFICATE blocks", blk.Type)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("certificate file is not a PEM-encoded certificate")
	}
	return nil
}

func trimNewline(b []byte) []byte {
	return []byte(strings.TrimRight(string(b), "\r\n"))
}
