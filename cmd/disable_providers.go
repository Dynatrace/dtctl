package cmd

func init() {
	disableCmd.AddCommand(disableAWSProviderCmd)
	disableCmd.AddCommand(disableAzureProviderCmd)
	disableCmd.AddCommand(disableGCPProviderCmd)
}
