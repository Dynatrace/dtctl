package apply

import (
	"fmt"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestAzureHintsReadTheResponseBody(t *testing.T) {
	wrap := func(details string) error {
		return fmt.Errorf("update: %w", httpclient.NewAPIError(400, "Constraints violated.", details))
	}

	cfg := wrap("was unable to be validated with validator builtin:cloud.azure.connection/azureConfiguration")
	if !isAzureConfigValidationError(cfg) || isAzureFederationError(cfg) {
		t.Errorf("config validation error misclassified")
	}
	for _, code := range []string{"AADSTS70025", "AADSTS700213"} {
		e := wrap("failed: " + code + " no federated credential")
		if !isAzureFederationError(e) || isAzureConfigValidationError(e) {
			t.Errorf("%s misclassified", code)
		}
	}
	if isAzureConfigValidationError(wrap("other")) || isAzureFederationError(wrap("other")) {
		t.Error("unrelated error matched")
	}
}
