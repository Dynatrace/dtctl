package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

type azureConnectionTableRow struct {
	Name     string `table:"NAME"`
	Type     string `table:"TYPE"`
	ObjectID string `table:"ID"`
}

func useAzureConnectionTableView(ctx context.Context) bool {
	return outputFormat(ctx) == "" || outputFormat(ctx) == "table" || outputFormat(ctx) == "wide"
}

func toAzureConnectionTableRow(item *azureconnection.AzureConnection) azureConnectionTableRow {
	return azureConnectionTableRow{
		Name:     item.Name,
		Type:     item.Type,
		ObjectID: item.ObjectID,
	}
}

func toAzureConnectionTableRows(items []azureconnection.AzureConnection) []azureConnectionTableRow {
	rows := make([]azureConnectionTableRow, 0, len(items))
	for i := range items {
		rows = append(rows, toAzureConnectionTableRow(&items[i]))
	}
	return rows
}

var getAzureProviderCmd = newGetAzureProviderCmd()

func newGetAzureProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "azure",
		Short: "Get Azure resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

// getAzureConnectionCmd retrieves Azure connections (formerly HAS credentials)
var getAzureConnectionCmd = newGetAzureConnectionCmd()

func newGetAzureConnectionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "connections [id]",
		Aliases: []string{"connection"},
		Short:   "Get Azure connections",
		Long:    `Get one or more Azure connections (authentication credentials).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := azureconnection.NewHandler(c)

			if len(args) > 0 {
				identifier := args[0]

				item, err := handler.FindByName(identifier)
				if err == nil {
					if useAzureConnectionTableView(cmdContext(cmd)) {
						row := toAzureConnectionTableRow(item)
						return printer.Print(row)
					}
					return printer.Print(item)
				}

				if strings.Contains(err.Error(), "not found") {
					item, err = handler.Get(identifier)
					if err != nil {
						return fmt.Errorf("connection with name or ID %q not found", identifier)
					}
					if useAzureConnectionTableView(cmdContext(cmd)) {
						row := toAzureConnectionTableRow(item)
						return printer.Print(row)
					}
					return printer.Print(item)
				}
				return err
			}

			items, err := handler.List()
			if err != nil {
				return err
			}
			if useAzureConnectionTableView(cmdContext(cmd)) {
				return printer.PrintList(toAzureConnectionTableRows(items))
			}
			return printer.PrintList(items)
		},
	}
	stability.MarkStable(c)
	return c
}

// getAzureMonitoringConfigCmd retrieves Azure monitoring configurations
var getAzureMonitoringConfigCmd = newGetAzureMonitoringConfigCmd()

func newGetAzureMonitoringConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config", "monitoring-configs"},
		Short:   "Get Azure monitoring configurations",
		Long:    `Get one or more Azure monitoring configurations.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := azuremonitoringconfig.NewHandler(c)

			if len(args) > 0 {
				identifier := args[0]

				item, err := handler.FindByName(identifier)
				if err == nil {
					return printer.Print(item)
				}

				if strings.Contains(err.Error(), "not found") {
					item, err = handler.Get(identifier)
					if err != nil {
						return fmt.Errorf("monitoring config with name/description or ID %q not found", identifier)
					}
					return printer.Print(item)
				}
				return err
			}

			items, err := handler.List()
			if err != nil {
				return err
			}
			return printer.PrintList(items)
		},
	}
	stability.MarkStable(c)
	return c
}

// getAzureMonitoringConfigLocationsCmd retrieves available Azure monitoring config locations from extension schema
var getAzureMonitoringConfigLocationsCmd = newGetAzureMonitoringConfigLocationsCmd()

func newGetAzureMonitoringConfigLocationsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring-locations",
		Aliases: []string{"monitoring-location"},
		Short:   "Get available Azure monitoring config locations",
		Long:    `Get available Azure regions for Azure monitoring configuration based on the latest extension schema.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := azuremonitoringconfig.NewHandler(c)

			locations, err := handler.ListAvailableLocations()
			if err != nil {
				return err
			}

			return printer.PrintList(locations)
		},
	}
	stability.MarkStable(c)
	return c
}

// getAzureMonitoringConfigFeatureSetsCmd retrieves available Azure monitoring config feature sets from extension schema
var getAzureMonitoringConfigFeatureSetsCmd = newGetAzureMonitoringConfigFeatureSetsCmd()

func newGetAzureMonitoringConfigFeatureSetsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring-feature-sets",
		Aliases: []string{"monitoring-feature-set"},
		Short:   "Get available Azure monitoring config feature sets",
		Long:    `Get available FeatureSetsType values for Azure monitoring configuration based on the latest extension schema.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := azuremonitoringconfig.NewHandler(c)

			featureSets, err := handler.ListAvailableFeatureSets()
			if err != nil {
				return err
			}

			return printer.PrintList(featureSets)
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	getCmd.AddCommand(getAzureProviderCmd)

	getAzureProviderCmd.AddCommand(getAzureConnectionCmd)
	getAzureProviderCmd.AddCommand(getAzureMonitoringConfigCmd)
	getAzureProviderCmd.AddCommand(getAzureMonitoringConfigLocationsCmd)
	getAzureProviderCmd.AddCommand(getAzureMonitoringConfigFeatureSetsCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
