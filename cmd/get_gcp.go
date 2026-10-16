package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

type gcpConnectionTableRow struct {
	Name             string `table:"NAME"`
	Type             string `table:"TYPE"`
	Principal        string `table:"PRINCIPAL"`
	ServiceAccountID string `table:"SERVICE_ACCOUNT"`
	ObjectID         string `table:"ID"`
}

func useGCPConnectionTableView(ctx context.Context) bool {
	return outputFormat(ctx) == "" || outputFormat(ctx) == "table" || outputFormat(ctx) == "wide"
}

func toGCPConnectionTableRow(item *gcpconnection.GCPConnection) gcpConnectionTableRow {
	return gcpConnectionTableRow{
		Name:             item.Name,
		Type:             item.Type,
		Principal:        item.Principal,
		ServiceAccountID: item.ServiceAccountID,
		ObjectID:         item.ObjectID,
	}
}

func toGCPConnectionTableRows(items []gcpconnection.GCPConnection) []gcpConnectionTableRow {
	rows := make([]gcpConnectionTableRow, 0, len(items))
	for i := range items {
		rows = append(rows, toGCPConnectionTableRow(&items[i]))
	}
	return rows
}

var getGCPConnectionCmd = newGetGCPConnectionCmd()

func newGetGCPConnectionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "connections [id]",
		Aliases: []string{"connection"},
		Short:   "Get GCP connections",
		Long:    `Get one or more GCP connections (authentication credentials).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := gcpconnection.NewHandler(c)

			if len(args) > 0 {
				identifier := args[0]

				item, err := handler.FindByName(identifier)
				if err == nil {
					if useGCPConnectionTableView(cmdContext(cmd)) {
						row := toGCPConnectionTableRow(item)
						return printer.Print(row)
					}
					return printer.Print(item)
				}

				if strings.Contains(strings.ToLower(err.Error()), "not found") {
					item, err = handler.Get(identifier)
					if err != nil {
						return fmt.Errorf("connection with name or ID %q not found", identifier)
					}
					if useGCPConnectionTableView(cmdContext(cmd)) {
						row := toGCPConnectionTableRow(item)
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
			if useGCPConnectionTableView(cmdContext(cmd)) {
				return printer.PrintList(toGCPConnectionTableRows(items))
			}
			return printer.PrintList(items)
		},
	}
	stability.MarkStable(c)
	return c
}

var getGCPConnectionPrincipalCmd = newGetGCPConnectionPrincipalCmd()

func newGetGCPConnectionPrincipalCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "principal",
		Short: "Get Dynatrace GCP principal",
		Long:  `Get the Dynatrace-managed GCP principal used as consumer in GCP connection impersonation setup.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := gcpconnection.NewHandler(c)

			principal, err := handler.GetDynatracePrincipal()
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "not found") {
					return fmt.Errorf("dynatrace gcp principal not found yet; run 'dtctl create gcp connection <name> --serviceAccountId <service-account-email>' to initialize it")
				}
				return err
			}

			return printer.Print(principal)
		},
	}
	stability.MarkStable(c)
	return c
}

var getGCPMonitoringConfigCmd = newGetGCPMonitoringConfigCmd()

func newGetGCPMonitoringConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config", "monitoring-configs"},
		Short:   "Get GCP monitoring configurations",
		Long:    `Get one or more GCP monitoring configurations.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := gcpmonitoringconfig.NewHandler(c)

			if len(args) > 0 {
				identifier := args[0]

				item, err := handler.FindByName(identifier)
				if err == nil {
					return printer.Print(item)
				}

				if strings.Contains(strings.ToLower(err.Error()), "not found") {
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

var getGCPMonitoringConfigLocationsCmd = newGetGCPMonitoringConfigLocationsCmd()

func newGetGCPMonitoringConfigLocationsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring-locations",
		Aliases: []string{"monitoring-location"},
		Short:   "Get available GCP monitoring config locations",
		Long:    `Get available Google Cloud regions for monitoring configuration based on the latest extension schema.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := gcpmonitoringconfig.NewHandler(c)

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

var getGCPMonitoringConfigFeatureSetsCmd = newGetGCPMonitoringConfigFeatureSetsCmd()

func newGetGCPMonitoringConfigFeatureSetsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring-feature-sets",
		Aliases: []string{"monitoring-feature-set"},
		Short:   "Get available GCP monitoring config feature sets",
		Long:    `Get available FeatureSetsType values for GCP monitoring configuration based on the latest extension schema.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := gcpmonitoringconfig.NewHandler(c)

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
	getGCPConnectionCmd.AddCommand(getGCPConnectionPrincipalCmd)
	getGCPProviderCmd.AddCommand(getGCPConnectionCmd)
	getGCPProviderCmd.AddCommand(getGCPMonitoringConfigCmd)
	getGCPProviderCmd.AddCommand(getGCPMonitoringConfigLocationsCmd)
	getGCPProviderCmd.AddCommand(getGCPMonitoringConfigFeatureSetsCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
