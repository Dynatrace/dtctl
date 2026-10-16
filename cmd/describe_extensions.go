package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/extension"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// extensionDescription is a rich struct for JSON/YAML output of describe extension.
// FeatureSets is []string (names only) by default; map[string][]string (names → metric keys)
// when --feature-set-metrics is set.
type extensionDescription struct {
	Name                string                        `json:"name" yaml:"name"`
	Version             string                        `json:"version" yaml:"version"`
	Author              string                        `json:"author,omitempty" yaml:"author,omitempty"`
	MinDynatraceVersion string                        `json:"minDynatraceVersion,omitempty" yaml:"minDynatraceVersion,omitempty"`
	MinEECVersion       string                        `json:"minEECVersion,omitempty" yaml:"minEECVersion,omitempty"`
	FileHash            string                        `json:"fileHash,omitempty" yaml:"fileHash,omitempty"`
	DataSources         []string                      `json:"dataSources,omitempty" yaml:"dataSources,omitempty"`
	FeatureSets         interface{}                   `json:"featureSets,omitempty" yaml:"featureSets,omitempty"`
	Variables           []extension.ExtensionVariable `json:"variables,omitempty" yaml:"variables,omitempty"`
	ActiveVersion       string                        `json:"activeVersion,omitempty" yaml:"activeVersion,omitempty"`
	AvailableVersions   []string                      `json:"availableVersions,omitempty" yaml:"availableVersions,omitempty"`
	MonitoringConfigs   []monitoringConfigSummary     `json:"monitoringConfigurations,omitempty" yaml:"monitoringConfigurations,omitempty"`
}

type monitoringConfigSummary struct {
	ObjectID    string `json:"objectId" yaml:"objectId"`
	Scope       string `json:"scope,omitempty" yaml:"scope,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// describeExtensionCmd shows detailed info about an extension
var describeExtensionCmd = newDescribeExtensionCmd()

func newDescribeExtensionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "extension <extension-name>",
		Aliases: []string{"ext"},
		Short:   "Show details of an Extensions 2.0 extension",
		Long: `Show detailed information about an Extensions 2.0 extension including versions,
data sources, feature sets, and environment configuration.

Examples:
  # Describe an extension (shows active version details)
  dtctl describe extension com.dynatrace.extension.host-monitoring

  # Describe a specific version
  dtctl describe extension com.dynatrace.extension.host-monitoring --version 1.2.3

  # Show only the monitoring configuration schema for a specific version
  dtctl describe extension com.dynatrace.extension.host-monitoring --version 1.2.3 --monitoring-configuration-schema

  # List active gate groups available for a specific version
  dtctl describe extension com.dynatrace.extension.host-monitoring --version 1.2.3 --active-gate-groups

  # Show alert templates bundled in an extension
  dtctl describe extension com.dynatrace.extension.postgres --version 3.0.12 --assets=alert_templates

  # Show multiple asset types
  dtctl describe extension com.dynatrace.extension.postgres --version 3.0.12 --assets=alert_templates,smartscape
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			extensionName := args[0]
			versionFlag, _ := cmd.Flags().GetString("version")
			monConfigSchema, _ := cmd.Flags().GetBool("monitoring-configuration-schema")
			activeGateGroups, _ := cmd.Flags().GetBool("active-gate-groups")
			noFluff, _ := cmd.Flags().GetBool("no-fluff")
			assetsFlag, _ := cmd.Flags().GetString("assets")
			fullAssets, _ := cmd.Flags().GetBool("full")
			featureSetMetrics, _ := cmd.Flags().GetBool("feature-set-metrics")

			if monConfigSchema && activeGateGroups {
				return fmt.Errorf("--monitoring-configuration-schema and --active-gate-groups are mutually exclusive")
			}
			if noFluff && !monConfigSchema {
				return fmt.Errorf("--no-fluff only applies to --monitoring-configuration-schema")
			}
			if fullAssets && assetsFlag == "" {
				return fmt.Errorf("--full requires --assets")
			}
			if outputFormat(cmdContext(cmd)) == "zip" {
				return fmt.Errorf("-o zip is not supported on describe extension; use 'dtctl download extension <extension-name> --version <version>'")
			}

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := extension.NewHandler(c)

			// List all available versions
			versions, err := handler.Get(extensionName)
			if err != nil {
				return err
			}

			// Find the active version from the version list
			var activeVersion string
			for _, v := range versions.Items {
				if v.Active {
					activeVersion = v.Version
					break
				}
			}

			// Determine which version to describe
			targetVersion := versionFlag
			if targetVersion == "" {
				targetVersion = activeVersion
			}

			// If no target version, use the latest version from the list
			if targetVersion == "" && len(versions.Items) > 0 {
				targetVersion = versions.Items[0].Version
			}

			if targetVersion == "" {
				return fmt.Errorf("no versions found for extension %q", extensionName)
			}

			// --assets: download zip and display specific asset types
			if assetsFlag != "" {
				assetTypes := splitCSVList(assetsFlag)
				data, err := handler.Download(extensionName, targetVersion)
				if err != nil {
					return err
				}
				result, err := extension.ParseAssets(data, assetTypes, fullAssets)
				if err != nil {
					return err
				}
				if outputFormat(cmdContext(cmd)) == "" || outputFormat(cmdContext(cmd)) == "table" {
					printAssetsTable(cmdContext(cmd), result, fullAssets)
					return nil
				}
				enrichAgent(printer, "describe", "extension")
				return printer.Print(result)
			}

			// --monitoring-configuration-schema: output only the JSON Schema for monitoring configs
			if monConfigSchema {
				schema, err := handler.GetMonitoringConfigurationSchema(extensionName, targetVersion)
				if err != nil {
					return err
				}
				var schemaObj interface{}
				if err := json.Unmarshal(schema, &schemaObj); err != nil {
					return fmt.Errorf("failed to parse schema: %w", err)
				}
				if noFluff {
					schemaObj = extension.StripSchemaFluff(schemaObj)
				}
				// Table format has no structured columns for an arbitrary JSON Schema,
				// so print it as indented JSON directly. enrichAgent is skipped because
				// there is no printer involved.
				if outputFormat(cmdContext(cmd)) == "table" {
					indented, err := json.MarshalIndent(schemaObj, "", "  ")
					if err != nil {
						return fmt.Errorf("failed to format schema: %w", err)
					}
					fmt.Fprintln(currentStdout(cmdContext(cmd)), string(indented))
					return nil
				}
				enrichAgent(printer, "describe", "extension")
				return printer.Print(schemaObj)
			}

			// --active-gate-groups: output only the active gate groups for this version
			if activeGateGroups {
				groups, err := handler.GetActiveGateGroups(extensionName, targetVersion)
				if err != nil {
					return err
				}
				if outputFormat(cmdContext(cmd)) == "table" {
					if len(groups.Items) == 0 {
						fmt.Fprintln(currentStdout(cmdContext(cmd)), "No active gate groups found.")
						return nil
					}
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), fmt.Sprintf("Active Gate Groups (%d):", len(groups.Items)))
					for _, g := range groups.Items {
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  %s  (available: %d)\n", g.GroupName, g.AvailableActiveGates)
						for _, ag := range g.ActiveGates {
							var errList []interface{}
							_ = json.Unmarshal(ag.Errors, &errList)
							if len(errList) > 0 {
								errBytes, _ := json.Marshal(errList)
								fmt.Fprintf(currentStdout(cmdContext(cmd)), "    - id: %d  errors: %s\n", ag.ID, string(errBytes))
							} else {
								fmt.Fprintf(currentStdout(cmdContext(cmd)), "    - id: %d\n", ag.ID)
							}
						}
					}
					return nil
				}
				enrichAgent(printer, "describe", "extension")
				return printer.PrintList(groups.Items)
			}

			// Get detailed information for the target version
			details, err := handler.GetVersion(extensionName, targetVersion)
			if err != nil {
				return err
			}

			// Get monitoring configurations summary
			var configSummaries []monitoringConfigSummary
			configs, configErr := handler.ListMonitoringConfigurations(extensionName, "", 0)
			if configErr == nil {
				for _, cfg := range configs.Items {
					summary := monitoringConfigSummary{
						ObjectID: cfg.ObjectID,
						Scope:    cfg.Scope,
					}
					if cfg.Value != nil {
						var val map[string]interface{}
						if err := json.Unmarshal(cfg.Value, &val); err == nil {
							if enabled, ok := val["enabled"].(bool); ok {
								summary.Enabled = &enabled
							}
							if desc, ok := val["description"].(string); ok && desc != "" {
								summary.Description = desc
							}
						}
					}
					configSummaries = append(configSummaries, summary)
				}
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 16
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", details.ExtensionName)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%s", details.Version)

				if details.Author.Name != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Author:", w, "%s", details.Author.Name)
				}
				if details.MinDynatraceVersion != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Min Dynatrace:", w, "%s", details.MinDynatraceVersion)
				}
				if details.MinEECVersion != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Min EEC:", w, "%s", details.MinEECVersion)
				}
				if details.FileHash != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "File Hash:", w, "%s", details.FileHash)
				}
				if len(details.DataSources) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Data Sources:", w, "%s", strings.Join(details.DataSources, ", "))
				}
				if len(details.FeatureSets) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Feature Sets:")
					for _, fs := range details.FeatureSets {
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  - %s\n", fs)
						if featureSetMetrics {
							if detail, ok := details.FeatureSetDetails[fs]; ok {
								for _, m := range detail.Metrics {
									switch {
									case m.Metadata != nil && m.Metadata.DisplayName != "" && m.Metadata.Unit != "":
										fmt.Fprintf(currentStdout(cmdContext(cmd)), "      %s - %s (%s)\n", m.Key, m.Metadata.DisplayName, m.Metadata.Unit)
									case m.Metadata != nil && m.Metadata.DisplayName != "":
										fmt.Fprintf(currentStdout(cmdContext(cmd)), "      %s - %s\n", m.Key, m.Metadata.DisplayName)
									default:
										fmt.Fprintf(currentStdout(cmdContext(cmd)), "      %s\n", m.Key)
									}
								}
							}
						}
					}
				}
				if len(details.Variables) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Variables:")
					for _, v := range details.Variables {
						displayName := v.Name
						if v.DisplayName != "" {
							displayName = v.DisplayName
						}
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  - %s (%s)\n", displayName, v.Type)
					}
				}
				if activeVersion != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Active Version:", w, "%s", activeVersion)
				}
				if len(versions.Items) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Available Versions:")
					for _, v := range versions.Items {
						marker := "  "
						if activeVersion == v.Version {
							marker = "* "
						}
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  %s%s\n", marker, v.Version)
					}
				}
				if len(configSummaries) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), fmt.Sprintf("Monitoring Configurations: %d", len(configSummaries)))
					for _, cfg := range configSummaries {
						scope := cfg.Scope
						if scope == "" {
							scope = "(environment)"
						}
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  - %s  scope=%s\n", cfg.ObjectID, scope)
						if cfg.Enabled != nil {
							fmt.Fprintf(currentStdout(cmdContext(cmd)), "    enabled: %v\n", *cfg.Enabled)
						}
						if cfg.Description != "" {
							fmt.Fprintf(currentStdout(cmdContext(cmd)), "    description: %v\n", cfg.Description)
						}
					}
				}
				return nil
			}

			var availableVersions []string
			for _, v := range versions.Items {
				availableVersions = append(availableVersions, v.Version)
			}

			desc := &extensionDescription{
				Name:                details.ExtensionName,
				Version:             details.Version,
				Author:              details.Author.Name,
				MinDynatraceVersion: details.MinDynatraceVersion,
				MinEECVersion:       details.MinEECVersion,
				FileHash:            details.FileHash,
				DataSources:         details.DataSources,
				FeatureSets:         buildFeatureSetsOutput(details, featureSetMetrics),
				Variables:           details.Variables,
				ActiveVersion:       activeVersion,
				AvailableVersions:   availableVersions,
				MonitoringConfigs:   configSummaries,
			}

			enrichAgent(printer, "describe", "extension")
			return printer.Print(desc)
		},
	}
	c.Flags().String("version", "", "Show details for a specific extension version")
	c.Flags().Bool("monitoring-configuration-schema", false, "Output only the monitoring configuration schema for this extension version")
	c.Flags().Bool("active-gate-groups", false, "List active gate groups available for this extension version")
	c.Flags().Bool("no-fluff", false, "Strip documentation, customMessage, and displayName fields from schema output (use with --monitoring-configuration-schema)")
	c.Flags().String("assets", "", "Comma-separated asset types to show from the extension package. Supported: alert_templates, smartscape")
	c.Flags().Bool("full", false, "Show complete file content for each asset (use with --assets)")
	c.Flags().Bool("feature-set-metrics", false, "Show metrics available in each feature set")
	stability.MarkStable(c)
	return c
}

// printAssetsTable renders an AssetResult to stdout in human-readable table format.
func printAssetsTable(ctx context.Context, result *extension.AssetResult, full bool) {
	if result.AlertTemplates != nil {
		output.FprintDescribeSection(currentStdout(ctx), fmt.Sprintf("Alert Templates (%d):", len(result.AlertTemplates)))
		if len(result.AlertTemplates) == 0 {
			fmt.Fprintln(currentStdout(ctx), "  (none)")
		} else if !full {
			fmt.Fprintf(currentStdout(ctx), "  %-50s  %-20s  %s\n", "NAME", "EVENT_TYPE", "ENABLED")
		}
		for _, a := range result.AlertTemplates {
			if full {
				printAssetContent(ctx, a.File, a.Content)
			} else {
				enabled := "-"
				if a.Enabled != nil {
					enabled = fmt.Sprintf("enabled=%v", *a.Enabled)
				}
				fmt.Fprintf(currentStdout(ctx), "  %-50s  %-20s  %s\n", a.Name, a.EventType, enabled)
			}
		}
	}

	if result.Smartscape != nil {
		output.FprintDescribeSection(currentStdout(ctx), fmt.Sprintf("Smartscape Nodes (%d):", len(result.Smartscape.Nodes)))
		if len(result.Smartscape.Nodes) == 0 {
			fmt.Fprintln(currentStdout(ctx), "  (none)")
		} else if !full {
			fmt.Fprintf(currentStdout(ctx), "  %-35s  %-45s  %s\n", "NODE_TYPE", "ID_FIELD", "DESCRIPTION")
		}
		for _, n := range result.Smartscape.Nodes {
			if full {
				printAssetContent(ctx, n.NodeType, n.Content)
			} else {
				fmt.Fprintf(currentStdout(ctx), "  %-35s  %-45s  %s\n", n.NodeType, n.NodeIDFieldName, n.Description)
			}
		}

		fmt.Fprintln(currentStdout(ctx))
		output.FprintDescribeSection(currentStdout(ctx), fmt.Sprintf("Smartscape Edges (%d):", len(result.Smartscape.Edges)))
		if len(result.Smartscape.Edges) == 0 {
			fmt.Fprintln(currentStdout(ctx), "  (none)")
		} else {
			fmt.Fprintf(currentStdout(ctx), "  %-35s  %-15s  %s\n", "FROM", "EDGE_TYPE", "TO")
		}
		for _, e := range result.Smartscape.Edges {
			fmt.Fprintf(currentStdout(ctx), "  %-35s  %-15s  %s\n", e.SourceType, e.EdgeType, e.TargetType)
		}
	}
}

// printAssetContent prints indented JSON content for a named asset (used with --full).
func printAssetContent(ctx context.Context, label string, content interface{}) {
	indented, err := json.MarshalIndent(content, "  ", "  ")
	if err != nil {
		fmt.Fprintf(currentStdout(ctx), "  %s\n  (could not format content)\n", label)
		return
	}
	fmt.Fprintf(currentStdout(ctx), "  %s\n  %s\n", label, indented)
}

// buildFeatureSetsOutput shapes the featureSets field for JSON/YAML output.
//
// Without --feature-set-metrics it returns a plain []string of feature-set names;
// with the flag it returns a map of name → metric objects (key + metadata).
//
// It returns an untyped nil when the extension has no feature sets so that the
// `omitempty` tag actually drops the field: a non-nil empty slice or map boxed
// into an interface{} is NOT considered empty by encoding/json and would
// otherwise serialize as "featureSets": null / {} instead of being omitted.
func buildFeatureSetsOutput(details *extension.ExtensionDetails, withMetrics bool) interface{} {
	if len(details.FeatureSets) == 0 {
		return nil
	}
	if !withMetrics {
		return details.FeatureSets
	}
	fsMap := make(map[string][]extension.FeatureSetMetric, len(details.FeatureSets))
	for _, fs := range details.FeatureSets {
		metrics := details.FeatureSetDetails[fs].Metrics
		if metrics == nil {
			metrics = []extension.FeatureSetMetric{}
		}
		fsMap[fs] = metrics
	}
	return fsMap
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
