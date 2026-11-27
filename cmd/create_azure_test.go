package cmd

import (
	"strings"
	"testing"
)

func TestCreateAzureConnectionFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing --name and --type",
			args:    []string{"create", "azure", "connection"},
			wantErr: "required flag(s)",
		},
		{
			name:    "missing --type",
			args:    []string{"create", "azure", "connection", "--name", "my-conn"},
			wantErr: "required flag(s)",
		},
		{
			name:    "missing --name",
			args:    []string{"create", "azure", "connection", "--type", "clientSecret"},
			wantErr: "required flag(s)",
		},
		{
			name:    "unsupported type",
			args:    []string{"create", "azure", "connection", "--name", "my-conn", "--type", "unsupported"},
			wantErr: "unsupported --type",
		},
		{
			name: "--directory-id rejected for federatedIdentityCredential",
			args: []string{"create", "azure", "connection",
				"--name", "my-conn",
				"--type", "federatedIdentityCredential",
				"--directory-id", "some-id",
			},
			wantErr: "only supported for --type clientSecret",
		},
		{
			name: "--application-id rejected for federatedIdentityCredential",
			args: []string{"create", "azure", "connection",
				"--name", "my-conn",
				"--type", "federatedIdentityCredential",
				"--application-id", "some-id",
			},
			wantErr: "only supported for --type clientSecret",
		},
		{
			name: "--client-secret rejected for federatedIdentityCredential",
			args: []string{"create", "azure", "connection",
				"--name", "my-conn",
				"--type", "federatedIdentityCredential",
				"--client-secret", "some-secret",
			},
			wantErr: "only supported for --type clientSecret",
		},
		{
			name: "--issuer rejected for clientSecret",
			args: []string{"create", "azure", "connection",
				"--name", "my-conn",
				"--type", "clientSecret",
				"--issuer", "https://custom.token.example.com",
			},
			wantErr: "--issuer is only supported for --type federatedIdentityCredential",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetCmdFlags(createAzureConnectionCmd)

			rootCmd.SetArgs(tt.args)
			err := rootCmd.Execute()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestUpdateAzureConnectionFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "no credential flags",
			args:    []string{"update", "azure", "connection", "--name", "my-conn"},
			wantErr: "at least one of --directory-id, --application-id, or --client-secret is required",
		},
		{
			name:    "no name and no id arg",
			args:    []string{"update", "azure", "connection", "--client-secret", "s"},
			wantErr: "provide connection ID argument or --name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetCmdFlags(updateAzureConnectionCmd)

			rootCmd.SetArgs(tt.args)
			err := rootCmd.Execute()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}
