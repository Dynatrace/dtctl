package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/dynatrace-oss/dtctl/pkg/apply"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/diagnostic"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	resapi "github.com/dynatrace-oss/dtctl/pkg/resources/api"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/suggest"
	"github.com/dynatrace-oss/dtctl/sdk/api/appengine"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// TestBuildSpanName verifies that buildSpanName correctly extracts the verb and
// resource from a variety of command-line argument slices, including those with
// leading global flags and their values.
func TestBuildSpanName(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, "dtctl"},
		{[]string{}, "dtctl"},
		{[]string{"get"}, "dtctl get"},
		{[]string{"get", "workflows"}, "dtctl get workflows"},
		// Flags interspersed before the verb should be skipped.
		{[]string{"--context", "prod", "get", "workflows"}, "dtctl get workflows"},
		{[]string{"--context=prod", "get", "workflows"}, "dtctl get workflows"},
		{[]string{"--plain", "get", "workflows"}, "dtctl get workflows"},
		{[]string{"-v", "get", "workflows"}, "dtctl get workflows"},
		// Short flags that take values: -o json should skip "json".
		{[]string{"-o", "json", "get", "workflows"}, "dtctl get workflows"},
		// Long flags that take values: --jq should skip the filter expression.
		{[]string{"--jq", ".name", "get", "workflows"}, "dtctl get workflows"},
		{[]string{"--jq=.name", "get", "workflows"}, "dtctl get workflows"},
		// Mixed short and long flags.
		{[]string{"-v", "--context", "prod", "get", "workflows"}, "dtctl get workflows"},
		// Extra positional args after verb+resource are not included.
		{[]string{"get", "workflows", "my-workflow"}, "dtctl get workflows"},
		// Multiple leading long flags (each with a separate value).
		{[]string{"--context", "prod", "--output", "json", "get", "workflows"}, "dtctl get workflows"},
		// Flags after the positional args don't affect the result.
		{[]string{"get", "workflows", "--output", "json"}, "dtctl get workflows"},
		// Value-taking flag at the end without a value — must not panic.
		{[]string{"--context"}, "dtctl"},
		{[]string{"get", "--context"}, "dtctl get"},
		// Short value-taking flag at the end without a value.
		{[]string{"-o"}, "dtctl"},
		{[]string{"get", "-o"}, "dtctl get"},
		// Short value-taking flag followed by another flag (not a value).
		{[]string{"-o", "--plain", "get", "workflows"}, "dtctl get workflows"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("args=%v", tt.args), func(t *testing.T) {
			got := buildSpanName(tt.args)
			if got != tt.want {
				t.Errorf("buildSpanName(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestGlobalFlags_Config tests the --config flag
func TestGlobalFlags_Config(t *testing.T) {
	tests := []struct {
		name       string
		configFile string
		wantErr    bool
	}{
		{
			name:       "custom config path",
			configFile: "custom-config.yaml",
			wantErr:    false,
		},
		{
			name:       "default config location",
			configFile: "",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			tmpDir := t.TempDir()
			var configPath string

			if tt.configFile != "" {
				configPath = filepath.Join(tmpDir, tt.configFile)
				cfg := config.NewConfig()
				cfg.SetContext("test", "https://test.dt.com", "test-token")
				_ = cfg.SetToken("test-token", "dt0c01.test")
				cfg.CurrentContext = "test"
				if err := cfg.SaveTo(configPath); err != nil {
					t.Fatalf("failed to save config: %v", err)
				}
			}

			// Reset viper state
			viper.Reset()

			// Set config file flag
			gFlags.cfgFile = configPath

			// Initialize config
			initConfig(context.Background())

			// Verify config was loaded if custom path provided
			if tt.configFile != "" && viper.ConfigFileUsed() != configPath {
				t.Errorf("Expected config file %s, got %s", configPath, viper.ConfigFileUsed())
			}
		})
	}
}

// TestGlobalFlags_Context tests the --context flag
func TestGlobalFlags_Context(t *testing.T) {
	tests := []struct {
		name        string
		context     string
		wantContext string
	}{
		{
			name:        "custom context",
			context:     "prod",
			wantContext: "prod",
		},
		{
			name:        "default context",
			context:     "",
			wantContext: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset
			viper.Reset()
			contextName = tt.context

			if tt.context != "" {
				viper.Set("context", tt.context)
			}

			got := viper.GetString("context")
			if got != tt.wantContext {
				t.Errorf("context = %v, want %v", got, tt.wantContext)
			}
		})
	}
}

// TestLoadConfig tests the LoadConfig function with --context flag override
func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name               string
		currentContext     string
		contextFlagValue   string
		envContextValue    string
		wantCurrentContext string
	}{
		{
			name:               "no context flag - use config default",
			currentContext:     "dev",
			contextFlagValue:   "",
			wantCurrentContext: "dev",
		},
		{
			name:               "context flag overrides config",
			currentContext:     "dev",
			contextFlagValue:   "prod",
			wantCurrentContext: "prod",
		},
		{
			name:               "context flag set to same as config",
			currentContext:     "staging",
			contextFlagValue:   "staging",
			wantCurrentContext: "staging",
		},
		{
			name:               "DTCTL_CONTEXT env overrides config",
			currentContext:     "dev",
			envContextValue:    "prod",
			wantCurrentContext: "prod",
		},
		{
			name:               "context flag beats DTCTL_CONTEXT env",
			currentContext:     "dev",
			contextFlagValue:   "staging",
			envContextValue:    "prod",
			wantCurrentContext: "staging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")

			cfg := config.NewConfig()
			cfg.SetContext("dev", "https://dev.dt.com", "dev-token")
			cfg.SetContext("staging", "https://staging.dt.com", "staging-token")
			cfg.SetContext("prod", "https://prod.dt.com", "prod-token")
			cfg.CurrentContext = tt.currentContext

			if err := cfg.SaveTo(configPath); err != nil {
				t.Fatalf("failed to save config: %v", err)
			}

			// Save original values and environment
			origCfgFile := cfgFile(context.Background())
			origContextName := contextName
			origEnvContext := os.Getenv("DTCTL_CONTEXT")
			defer func() {
				gFlags.cfgFile = origCfgFile
				contextName = origContextName
				if origEnvContext != "" {
					_ = os.Setenv("DTCTL_CONTEXT", origEnvContext)
				} else {
					_ = os.Unsetenv("DTCTL_CONTEXT")
				}
			}()

			// Unset environment variable to avoid interference, then apply
			// the test case's env override if any
			_ = os.Unsetenv("DTCTL_CONTEXT")
			if tt.envContextValue != "" {
				_ = os.Setenv("DTCTL_CONTEXT", tt.envContextValue)
			}

			// Reset state
			viper.Reset()
			gFlags.cfgFile = configPath
			contextName = tt.contextFlagValue

			// Initialize config
			initConfig(context.Background())

			// Load config with context override
			loadedCfg, err := LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}

			if loadedCfg.CurrentContext != tt.wantCurrentContext {
				t.Errorf("LoadConfig().CurrentContext = %v, want %v", loadedCfg.CurrentContext, tt.wantCurrentContext)
			}
		})
	}
}

// TestGlobalFlags_Output tests the --output/-o flag
func TestGlobalFlags_Output(t *testing.T) {
	validFormats := []string{"json", "yaml", "csv", "toon", "table", "wide"}

	for _, format := range validFormats {
		t.Run(format, func(t *testing.T) {
			viper.Reset()
			gFlags.outputFormat = format
			viper.Set("output", format)

			got := viper.GetString("output")
			if got != format {
				t.Errorf("output format = %v, want %v", got, format)
			}
		})
	}
}

// TestGlobalFlags_Verbose tests the --verbose/-v flag
func TestGlobalFlags_Verbose(t *testing.T) {
	tests := []struct {
		name      string
		verbosity int
		want      int
	}{
		{
			name:      "no verbose flag",
			verbosity: 0,
			want:      0,
		},
		{
			name:      "single -v",
			verbosity: 1,
			want:      1,
		},
		{
			name:      "double -vv",
			verbosity: 2,
			want:      2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.verbosity = tt.verbosity

			if verbosity(context.Background()) != tt.want {
				t.Errorf("gFlags.verbosity = %v, want %v", verbosity(context.Background()), tt.want)
			}
		})
	}
}

// TestGlobalFlags_DryRun tests the --dry-run flag
func TestGlobalFlags_DryRun(t *testing.T) {
	tests := []struct {
		name   string
		dryRun bool
		want   bool
	}{
		{
			name:   "dry-run enabled",
			dryRun: true,
			want:   true,
		},
		{
			name:   "dry-run disabled",
			dryRun: false,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.dryRun = tt.dryRun

			if dryRun(context.Background()) != tt.want {
				t.Errorf("gFlags.dryRun = %v, want %v", dryRun(context.Background()), tt.want)
			}
		})
	}
}

// TestGlobalFlags_PlainMode tests the --plain flag
func TestGlobalFlags_PlainMode(t *testing.T) {
	tests := []struct {
		name  string
		plain bool
		want  bool
	}{
		{
			name:  "plain mode enabled",
			plain: true,
			want:  true,
		},
		{
			name:  "plain mode disabled",
			plain: false,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.plainMode = tt.plain

			got := GetPlainMode()
			if got != tt.want {
				t.Errorf("GetPlainMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGlobalFlags_ChunkSize tests the --chunk-size flag
func TestGlobalFlags_ChunkSize(t *testing.T) {
	tests := []struct {
		name      string
		chunkSize int64
		want      int64
	}{
		{
			name:      "default chunk size",
			chunkSize: 500,
			want:      500,
		},
		{
			name:      "custom chunk size",
			chunkSize: 1000,
			want:      1000,
		},
		{
			name:      "first page only",
			chunkSize: 0,
			want:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.chunkSize = tt.chunkSize

			got := GetChunkSize()
			if got != tt.want {
				t.Errorf("GetChunkSize() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNewPrinter tests the NewPrinter helper function
func TestNewPrinter(t *testing.T) {
	tests := []struct {
		name         string
		outputFormat string
		plainMode    bool
	}{
		{
			name:         "json output",
			outputFormat: "json",
			plainMode:    false,
		},
		{
			name:         "yaml output",
			outputFormat: "yaml",
			plainMode:    false,
		},
		{
			name:         "table output with plain mode",
			outputFormat: "table",
			plainMode:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.outputFormat = tt.outputFormat
			gFlags.plainMode = tt.plainMode

			printer := NewPrinter()
			if printer == nil {
				t.Error("NewPrinter() returned nil")
			}
		})
	}
}

// TestInitConfig tests the initConfig function
func TestInitConfig(t *testing.T) {
	tests := []struct {
		name           string
		setupConfig    bool
		configFileName string
		wantConfigUsed bool
	}{
		{
			name:           "valid config file exists",
			setupConfig:    true,
			configFileName: "config.yaml",
			wantConfigUsed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			viper.Reset()

			if tt.setupConfig {
				configPath := filepath.Join(tmpDir, tt.configFileName)
				cfg := config.NewConfig()
				cfg.SetContext("test", "https://test.dt.com", "token")
				_ = cfg.SaveTo(configPath)
				gFlags.cfgFile = configPath
			} else {
				gFlags.cfgFile = ""
			}

			initConfig(context.Background())

			configUsed := viper.ConfigFileUsed()
			if tt.wantConfigUsed && configUsed == "" {
				t.Error("Expected config file to be used, but none was loaded")
			}
			if !tt.wantConfigUsed && configUsed != "" {
				t.Errorf("Expected no config file, but got %s", configUsed)
			}
		})
	}
}

// TestAgentModeAutoDetectWithExplicitJSON locks the matrix-11 finding: agents
// append `-o json` to nearly every call, and treating any explicit -o as an
// agent-mode opt-out silently disarmed every envelope affordance for exactly
// the audience it was built for. Explicit json must keep agent mode on; an
// explicit non-JSON format must still opt out.
func TestAgentModeAutoDetectWithExplicitJSON(t *testing.T) {
	origAgent, origNoAgent, origOutput := agentMode(context.Background()), noAgent(context.Background()), outputFormat(context.Background())
	outputFlag := rootCmd.PersistentFlags().Lookup("output")
	origChanged := outputFlag.Changed
	defer func() {
		gFlags.agentMode, gFlags.noAgent, gFlags.outputFormat = origAgent, origNoAgent, origOutput
		outputFlag.Changed = origChanged
		os.Unsetenv("AI_AGENT")
	}()
	os.Setenv("AI_AGENT", "1")

	cases := []struct {
		name    string
		format  string
		changed bool
		want    bool
	}{
		{"no explicit output", "table", false, true},
		{"explicit -o json", "json", true, true},
		{"explicit -o yaml", "yaml", true, false},
		{"explicit -o table", "table", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gFlags.agentMode, gFlags.noAgent = false, false
			gFlags.outputFormat = tc.format
			outputFlag.Changed = tc.changed
			initConfig(context.Background())
			if agentMode(context.Background()) != tc.want {
				t.Errorf("gFlags.agentMode = %v, want %v", agentMode(context.Background()), tc.want)
			}
		})
	}
}

// TestEnhanceFlagError tests flag error enhancement with suggestions
func TestEnhanceFlagError(t *testing.T) {
	tests := []struct {
		name        string
		errMsg      string
		wantContain string
	}{
		{
			name:        "unknown flag with close match",
			errMsg:      "unknown flag: --outpt",
			wantContain: "output",
		},
		{
			name:        "unknown shorthand flag",
			errMsg:      "unknown shorthand flag: 'x'",
			wantContain: "unknown flag --x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The enhanceFlagError function is called internally by cobra
			// We test it indirectly by verifying the error suggestion system works
			flags := collectFlags(rootCmd)

			if len(flags) == 0 {
				t.Error("Expected to collect flags from rootCmd, got none")
			}

			// Verify our global flags are in the list
			expectedFlags := []string{"config", "context", "output", "verbose", "plain", "chunk-size"}
			for _, expectedFlag := range expectedFlags {
				found := false
				for _, flag := range flags {
					if flag == expectedFlag {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected flag %s not found in collected flags", expectedFlag)
				}
			}
		})
	}
}

// TestVerbSynonyms locks the mapping from verbs agents guess (list, show, …)
// to real dtctl verbs, replacing the useless edit-distance fallback
// ("list" → "alias") observed in evals.
func TestVerbSynonyms(t *testing.T) {
	err := enhanceCommandError(rootCmd, fmt.Errorf(`unknown command "list" for "dtctl"`))
	var cmdErr *suggest.CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected CommandError, got %T: %v", err, err)
	}
	if cmdErr.Suggestion == nil || cmdErr.Suggestion.Value != "get" {
		t.Errorf("list should suggest get, got %+v", cmdErr.Suggestion)
	}
	if !strings.Contains(cmdErr.UsageHint, "dtctl get <resource>") {
		t.Errorf("usage hint missing: %q", cmdErr.UsageHint)
	}
	// The hint must reach the agent envelope.
	detail := errorToDetail(context.Background(), err)
	if len(detail.Suggestions) != 2 || !strings.Contains(detail.Suggestions[1], "dtctl get <resource>") {
		t.Errorf("envelope suggestions = %v", detail.Suggestions)
	}
	// Non-synonym verbs still go through edit-distance matching.
	err = enhanceCommandError(rootCmd, fmt.Errorf(`unknown command "quer" for "dtctl"`))
	if !errors.As(err, &cmdErr) || cmdErr.Suggestion == nil || cmdErr.Suggestion.Value != "query" {
		t.Errorf("quer should still suggest query, got %v", err)
	}
}

// TestAdviseFlag locks the special-cased flag guidance for flags agents carry
// over from other CLIs (--format, and --limit/--query on the query command).
func TestAdviseFlag(t *testing.T) {
	queryLike := &cobra.Command{Use: "query"}
	getLike := &cobra.Command{Use: "get"}
	cases := []struct {
		cmd         *cobra.Command
		flag        string
		wantNil     bool
		wantMessage string
		wantSuggest string
	}{
		{getLike, "format", false, "unknown flag --format", "output"},
		{queryLike, "limit", false, "append `| limit N`", ""},
		{queryLike, "query", false, "positional argument", ""},
		{getLike, "limit", true, "", ""},
		{queryLike, "outpt", true, "", ""},
	}
	for _, tc := range cases {
		fe := adviseFlag(tc.cmd, tc.flag)
		if tc.wantNil {
			if fe != nil {
				t.Errorf("adviseFlag(%s, --%s) = %v, want nil", tc.cmd.Name(), tc.flag, fe)
			}
			continue
		}
		if fe == nil {
			t.Fatalf("adviseFlag(%s, --%s) = nil", tc.cmd.Name(), tc.flag)
		}
		if !strings.Contains(fe.Message, tc.wantMessage) {
			t.Errorf("message %q missing %q", fe.Message, tc.wantMessage)
		}
		if tc.wantSuggest != "" && (fe.Suggestion == nil || fe.Suggestion.Value != tc.wantSuggest) {
			t.Errorf("suggestion = %+v, want %s", fe.Suggestion, tc.wantSuggest)
		}
	}
}

// TestDqlErrorAdviceTimeframe locks the INVALID_TIMEFRAME recovery hint
// (evals: --default-timeframe-start now-6h, missing the parentheses).
func TestDqlErrorAdviceTimeframe(t *testing.T) {
	s := dqlErrorAdvice(&sdkquery.QueryError{ErrorType: "INVALID_TIMEFRAME", Message: "INVALID_TIMEFRAME"})
	if len(s) != 1 || !strings.Contains(s[0], "now()-6h") {
		t.Errorf("advice = %v", s)
	}
}

// TestDqlErrorAdviceStreamNearMiss locks the UNKNOWN_DATA_OBJECT near-miss
// suggestions (evals: agents guessed usersession/usersessions/rum_events/
// dt.rum.sessions across ten calls without finding user.events, then
// reported "0 RUM events" as the measured answer).
func TestDqlErrorAdviceStreamNearMiss(t *testing.T) {
	cases := []struct {
		guess string
		want  string
	}{
		{"usersessions", "user.sessions"},
		{"rum_events", "user.sessions"},
		{"dt.rum.sessions", "user.sessions"},
		{"user_actions", "user.sessions"},
		{"dt.metrics", "metric.series"},
		{"vulnerabilities", "security.events"},
		{"traces", "spans"},
		{"bizevent", "bizevents"},
	}
	for _, tc := range cases {
		e := &sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT",
			Message: "UNKNOWN_DATA_OBJECT", Detail: tc.guess + " isn't a valid data object."}
		s := dqlErrorAdvice(e)
		if len(s) == 0 || !strings.Contains(s[0], tc.want) {
			t.Errorf("advice for %q = %v, want mention of %q", tc.guess, s, tc.want)
		}
		if !strings.Contains(s[0], "dt.system.data_objects") {
			t.Errorf("advice for %q lacks the catalog pointer: %v", tc.guess, s)
		}
	}
	// dt.entity.* guesses keep their dedicated census advice, not near-miss.
	e := &sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT",
		Message: "UNKNOWN_DATA_OBJECT", Detail: "dt.entity.database isn't a valid data object."}
	if s := dqlErrorAdvice(e); len(s) == 0 || !strings.Contains(s[0], "smartscapeNodes") {
		t.Errorf("dt.entity advice = %v", s)
	}
}

// TestDqlErrorAdviceClassicRelationships locks the redirect for the classic
// Environment-API relationship idiom (evals: repeated `fieldsAdd
// toRelationships` FIELD_DOES_NOT_EXIST failures, 28-call topology flail).
func TestDqlErrorAdviceClassicRelationships(t *testing.T) {
	for _, f := range []string{"toRelationships", "fromRelationships"} {
		e := &sdkquery.QueryError{ErrorType: "FIELD_DOES_NOT_EXIST",
			Message: "FIELD_DOES_NOT_EXIST", Detail: "The field " + f + " doesn't exist."}
		s := dqlErrorAdvice(e)
		if len(s) == 0 || !strings.Contains(s[0], "smartscapeEdges") {
			t.Errorf("advice for %s = %v", f, s)
		}
	}
	// Other unknown fields stay silent — no generic field advice exists.
	e := &sdkquery.QueryError{ErrorType: "FIELD_DOES_NOT_EXIST",
		Message: "FIELD_DOES_NOT_EXIST", Detail: "The field foo doesn't exist."}
	if s := dqlErrorAdvice(e); len(s) != 0 {
		t.Errorf("unexpected advice for unrelated field: %v", s)
	}
}

// TestEnhanceCommandError tests command error enhancement with suggestions
func TestEnhanceCommandError(t *testing.T) {
	subcommands := collectSubcommands(rootCmd)

	if len(subcommands) == 0 {
		t.Error("Expected to collect subcommands from rootCmd, got none")
	}

	// Verify common subcommands are present
	expectedCommands := []string{"get", "create", "apply", "delete", "query", "exec"}
	for _, expectedCmd := range expectedCommands {
		found := false
		for _, cmd := range subcommands {
			if cmd == expectedCmd {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected command %s not found in collected subcommands", expectedCmd)
		}
	}
}

// TestRequireSubcommand tests the requireSubcommand helper
func TestRequireSubcommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "no args provided",
			args:    []string{},
			wantErr: true,
		},
		{
			name:    "unknown resource type",
			args:    []string{"invalidresource"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireSubcommand(rootCmd, tt.args)

			if (err != nil) != tt.wantErr {
				t.Errorf("requireSubcommand() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestEnvironmentVariableBinding tests that persistent flags are bound to environment variables
func TestEnvironmentVariableBinding(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		envVal  string
		flagKey string
	}{
		{
			name:    "DTCTL_CONTEXT env var",
			envVar:  "DTCTL_CONTEXT",
			envVal:  "prod-context",
			flagKey: "context",
		},
		{
			name:    "DTCTL_OUTPUT env var",
			envVar:  "DTCTL_OUTPUT",
			envVal:  "json",
			flagKey: "output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variable
			os.Setenv(tt.envVar, tt.envVal)
			defer os.Unsetenv(tt.envVar)

			// Reset and reinitialize viper
			viper.Reset()
			viper.SetEnvPrefix("DTCTL")
			viper.AutomaticEnv()

			// Bind flag
			rootCmd.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
				if flag.Name == tt.flagKey {
					_ = viper.BindPFlag(tt.flagKey, flag)
				}
			})

			// Verify environment variable is read
			got := viper.GetString(tt.flagKey)
			if got != tt.envVal {
				t.Errorf("viper.GetString(%s) = %v, want %v", tt.flagKey, got, tt.envVal)
			}
		})
	}
}

// --- errorToDetail tests ---

func TestErrorToDetail_DiagnosticError(t *testing.T) {
	err := &diagnostic.Error{
		Operation:  "get workflows",
		StatusCode: 401,
		Message:    "authentication failed",
		RequestID:  "req-abc-123",
		Suggestions: []string{
			"Run 'dtctl auth login' to refresh your token",
		},
	}

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "auth_required" {
		t.Errorf("Code = %q, want %q", detail.Code, "auth_required")
	}
	if detail.Message != "authentication failed" {
		t.Errorf("Message = %q, want %q", detail.Message, "authentication failed")
	}
	if detail.Operation != "get workflows" {
		t.Errorf("Operation = %q, want %q", detail.Operation, "get workflows")
	}
	if detail.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want %d", detail.StatusCode, 401)
	}
	if detail.RequestID != "req-abc-123" {
		t.Errorf("RequestID = %q, want %q", detail.RequestID, "req-abc-123")
	}
	if len(detail.Suggestions) != 1 {
		t.Fatalf("Suggestions count = %d, want 1", len(detail.Suggestions))
	}
}

func TestErrorToDetail_DiagnosticErrorStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantCode   string
	}{
		{"400 bad request", 400, "bad_request"},
		{"401 unauthorized", 401, "auth_required"},
		{"403 forbidden", 403, "permission_denied"},
		{"404 not found", 404, "not_found"},
		{"409 conflict", 409, "conflict"},
		{"429 rate limited", 429, "rate_limited"},
		{"500 server error", 500, "server_error"},
		{"502 bad gateway", 502, "server_error"},
		{"0 no status code", 0, "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &diagnostic.Error{
				Operation:  "test",
				StatusCode: tt.statusCode,
				Message:    "test error",
			}
			detail := errorToDetail(context.Background(), err)
			if detail.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", detail.Code, tt.wantCode)
			}
		})
	}
}

func TestErrorToDetail_APIError(t *testing.T) {
	err := httpclient.NewAPIError(404, "not found", "workflow does not exist")

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "not_found" {
		t.Errorf("Code = %q, want %q", detail.Code, "not_found")
	}
	if want := "API error (404): not found - workflow does not exist"; detail.Message != want {
		t.Errorf("Message = %q, want %q", detail.Message, want)
	}
	if detail.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want %d", detail.StatusCode, 404)
	}
}

// TestSDKHTTPErrorIsTyped covers #498: an SDK HTTP error, wrapped by a
// handler with %w, must keep its documented code, status and exit code.
func TestSDKHTTPErrorIsTyped(t *testing.T) {
	tests := []struct {
		status   int
		wantCode string
		wantExit int
	}{
		{401, "auth_required", client.ExitAuthError},
		{403, "permission_denied", client.ExitPermissionError},
		{404, "not_found", client.ExitNotFoundError},
	}
	for _, tt := range tests {
		t.Run(tt.wantCode, func(t *testing.T) {
			err := fmt.Errorf("failed to get dashboard: %w",
				httpclient.NewAPIError(tt.status, "Configuration not found", ""))

			detail := errorToDetail(context.Background(), err)
			if detail.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", detail.Code, tt.wantCode)
			}
			if detail.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", detail.StatusCode, tt.status)
			}
			if !strings.HasPrefix(detail.Message, "failed to get dashboard: ") {
				t.Errorf("Message = %q, want the handler context kept", detail.Message)
			}
			if got := exitCodeForError(err); got != tt.wantExit {
				t.Errorf("exitCodeForError() = %d, want %d", got, tt.wantExit)
			}
		})
	}
}

// TestAPIIndexErrorsKeepTheirOwnExitCode pins the ordering exitCodeForError
// shares with errorToDetail: RegistryUnavailableError and SpecUnavailableError
// wrap an APIError but classify themselves, so the wrapped status must not
// decide the exit code. Answering "no API index here" with exit 4 would tell a
// script the API it asked about does not exist.
func TestAPIIndexErrorsKeepTheirOwnExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
		wantExit int
	}{
		{
			name: "unpublished index is not a missing resource",
			err: &resapi.RegistryUnavailableError{
				Path:       "/platform/metadata/v1/swagger-ui.json",
				StatusCode: 404,
				Err:        httpclient.NewAPIError(404, "Not Found", ""),
			},
			wantCode: "api_index_unavailable",
			wantExit: client.ExitError,
		},
		{
			name: "refused index is about the credential",
			err: &resapi.RegistryUnavailableError{
				Path:       "/platform/metadata/v1/swagger-ui.json",
				StatusCode: 403,
				Err:        httpclient.NewAPIError(403, "Forbidden", ""),
			},
			wantCode: "permission_denied",
			wantExit: client.ExitPermissionError,
		},
		{
			name: "unreadable specification is not a missing resource",
			err: &resapi.SpecUnavailableError{
				DocPath:    "/platform/widget/v1/openapi.yaml",
				StatusCode: 404,
				Err:        httpclient.NewAPIError(404, "Not Found", ""),
			},
			wantCode: "api_spec_unavailable",
			wantExit: client.ExitError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorToDetail(context.Background(), tt.err).Code; got != tt.wantCode {
				t.Errorf("Code = %q, want %q", got, tt.wantCode)
			}
			if got := exitCodeForError(tt.err); got != tt.wantExit {
				t.Errorf("exitCodeForError() = %d, want %d", got, tt.wantExit)
			}
		})
	}
}

func TestErrorToDetail_APIErrorWithoutDetails(t *testing.T) {
	err := httpclient.NewAPIError(500, "internal server error", "")

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "server_error" {
		t.Errorf("Code = %q, want %q", detail.Code, "server_error")
	}
	if want := "API error (500): internal server error"; detail.Message != want {
		t.Errorf("Message = %q, want %q", detail.Message, want)
	}
}

// TestErrorToDetail_FunctionExecutionError pins that a failure inside submitted
// function code does not classify as server_error. App Engine reports it with
// the non-standard status 540 or 541, and docs/AGENT_MODE.md tells an agent to
// retry a server_error with backoff — which, for a deterministic bug in the
// caller's own code, is an endless loop.
func TestErrorToDetail_FunctionExecutionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "javascript error",
			err:  &appengine.ExecutionError{StatusCode: 540, Message: "JavaScript error occurred", Body: "ReferenceError: x is not defined"},
		},
		{
			name: "runtime error",
			err:  &appengine.ExecutionError{StatusCode: 541, Message: "runtime error occurred", Body: ""},
		},
		{
			name: "wrapped by the caller",
			err:  fmt.Errorf("execute code: %w", &appengine.ExecutionError{StatusCode: 540, Message: "JavaScript error occurred"}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorToDetail(context.Background(), tt.err).Code; got != "function_error" {
				t.Errorf("Code = %q, want %q", got, "function_error")
			}
			if got := errorToDetail(context.Background(), tt.err).StatusCode; got != 0 {
				t.Errorf("StatusCode = %d, want 0 — 540 and 541 are not HTTP failures", got)
			}
			if got := exitCodeForError(tt.err); got != client.ExitError {
				t.Errorf("exitCodeForError() = %d, want %d", got, client.ExitError)
			}
		})
	}
}

func TestErrorToDetail_SafetyError(t *testing.T) {
	err := &safety.SafetyError{
		ContextName: "production",
		SafetyLevel: config.SafetyLevelReadOnly,
		Operation:   safety.OperationDelete,
		Reason:      "Context 'production' (readonly) does not allow delete operations",
		Suggestions: []string{
			"Switch to a context with write permissions",
		},
	}

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "safety_blocked" {
		t.Errorf("Code = %q, want %q", detail.Code, "safety_blocked")
	}
	if detail.Message != "Context 'production' (readonly) does not allow delete operations" {
		t.Errorf("Message = %q, want expected reason", detail.Message)
	}
	if len(detail.Suggestions) != 1 {
		t.Fatalf("Suggestions count = %d, want 1", len(detail.Suggestions))
	}
}

func TestErrorToDetail_CommandError(t *testing.T) {
	err := &suggest.CommandError{
		Command: "geet",
		Message: `unknown command "geet"`,
		Suggestion: &suggest.Suggestion{
			Value:    "get",
			Distance: 1,
		},
	}

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "unknown_command" {
		t.Errorf("Code = %q, want %q", detail.Code, "unknown_command")
	}
	if detail.Message != `unknown command "geet"` {
		t.Errorf("Message = %q, want expected message", detail.Message)
	}
	if len(detail.Suggestions) != 1 {
		t.Fatalf("Suggestions count = %d, want 1", len(detail.Suggestions))
	}
	if detail.Suggestions[0] != `did you mean "get"?` {
		t.Errorf("Suggestion = %q, want %q", detail.Suggestions[0], `did you mean "get"?`)
	}
}

func TestErrorToDetail_CommandErrorNoSuggestion(t *testing.T) {
	err := &suggest.CommandError{
		Command: "xyzzy",
		Message: `unknown command "xyzzy"`,
	}

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "unknown_command" {
		t.Errorf("Code = %q, want %q", detail.Code, "unknown_command")
	}
	if detail.Suggestions != nil {
		t.Errorf("Suggestions should be nil, got %v", detail.Suggestions)
	}
}

func TestErrorToDetail_FlagError(t *testing.T) {
	err := &suggest.FlagError{
		Flag:    "outpt",
		Message: "unknown flag --outpt",
		Suggestion: &suggest.Suggestion{
			Value:    "output",
			Distance: 1,
		},
	}

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "unknown_command" {
		t.Errorf("Code = %q, want %q", detail.Code, "unknown_command")
	}
	if len(detail.Suggestions) != 1 {
		t.Fatalf("Suggestions count = %d, want 1", len(detail.Suggestions))
	}
	if detail.Suggestions[0] != "did you mean --output?" {
		t.Errorf("Suggestion = %q, want %q", detail.Suggestions[0], "did you mean --output?")
	}
}

func TestErrorToDetail_GenericError(t *testing.T) {
	err := fmt.Errorf("something unexpected happened")

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "error" {
		t.Errorf("Code = %q, want %q", detail.Code, "error")
	}
	if detail.Message != "something unexpected happened" {
		t.Errorf("Message = %q, want %q", detail.Message, "something unexpected happened")
	}
}

func TestErrorToDetail_WrappedDiagnosticError(t *testing.T) {
	inner := &diagnostic.Error{
		Operation:  "apply dashboard",
		StatusCode: 403,
		Message:    "insufficient permissions",
	}
	wrapped := fmt.Errorf("command failed: %w", inner)

	detail := errorToDetail(context.Background(), wrapped)

	if detail.Code != "permission_denied" {
		t.Errorf("Code = %q, want %q (should unwrap diagnostic.Error)", detail.Code, "permission_denied")
	}
	if detail.Operation != "apply dashboard" {
		t.Errorf("Operation = %q, want %q", detail.Operation, "apply dashboard")
	}
}

func TestErrorToDetail_DiagnosticPrecedesAPIError(t *testing.T) {
	// diagnostic.Error wraps an httpclient.APIError — diagnostic should take precedence
	apiErr := httpclient.NewAPIError(404, "not found", "")
	diagErr := &diagnostic.Error{Operation: "get workflows", StatusCode: 404, Err: apiErr}

	detail := errorToDetail(context.Background(), diagErr)

	// Should use diagnostic.Error classification, not raw APIError
	if detail.Code != "not_found" {
		t.Errorf("Code = %q, want %q", detail.Code, "not_found")
	}
	if detail.Operation != "get workflows" {
		t.Errorf("Operation = %q, want %q", detail.Operation, "get workflows")
	}
}

func TestErrorToDetail_HookRejectedError(t *testing.T) {
	err := &apply.HookRejectedError{
		Command:  "node validate.js",
		ExitCode: 1,
		Stderr:   "validation failed: missing required field 'owner'",
	}

	detail := errorToDetail(context.Background(), err)

	if detail.Code != "hook_rejected" {
		t.Errorf("Code = %q, want %q", detail.Code, "hook_rejected")
	}
	if detail.Message != "pre-apply hook rejected the resource" {
		t.Errorf("Message = %q, want %q", detail.Message, "pre-apply hook rejected the resource")
	}
	if len(detail.Suggestions) != 2 {
		t.Fatalf("Suggestions count = %d, want 2", len(detail.Suggestions))
	}
	if detail.Suggestions[0] != "check hook stderr output for details" {
		t.Errorf("Suggestions[0] = %q, want %q", detail.Suggestions[0], "check hook stderr output for details")
	}
	if detail.Suggestions[1] != "use --no-hooks to skip pre-apply hooks" {
		t.Errorf("Suggestions[1] = %q, want %q", detail.Suggestions[1], "use --no-hooks to skip pre-apply hooks")
	}
}

func TestErrorToDetail_HookRejectedErrorWrapped(t *testing.T) {
	inner := &apply.HookRejectedError{
		Command:  "bash lint.sh",
		ExitCode: 2,
		Stderr:   "lint failed",
	}
	wrapped := fmt.Errorf("apply failed: %w", inner)

	detail := errorToDetail(context.Background(), wrapped)

	if detail.Code != "hook_rejected" {
		t.Errorf("Code = %q, want %q (should unwrap HookRejectedError)", detail.Code, "hook_rejected")
	}
}

// --- classifyGenericError tests ---

func TestClassifyGenericError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"context error", errors.New("no active context configured"), "context_error"},
		{"no context", errors.New("no context set"), "context_error"},
		{"config error", errors.New("failed to load config"), "config_error"},
		{"configuration error", errors.New("invalid configuration"), "config_error"},
		{"timeout error", errors.New("operation timed out"), "timeout"},
		{"timeout variant", errors.New("request timeout after 30s"), "timeout"},
		{"validation error", errors.New("validation failed for field name"), "validation_error"},
		{"invalid input", errors.New("invalid resource definition"), "validation_error"},
		{"generic error", errors.New("something went wrong"), "error"},
		{"empty error", errors.New(""), "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyGenericError(context.Background(), tt.err)
			if got != tt.wantCode {
				t.Errorf("classifyGenericError(%q) = %q, want %q", tt.err, got, tt.wantCode)
			}
		})
	}
}

// TestClassifyGenericError_DeadlineByType: a deadline whose wording names no
// timeout is classified "timeout" on a concurrent invocation, where it races
// the HTTP client timeout, and keeps its historical "error" everywhere else.
func TestClassifyGenericError_DeadlineByType(t *testing.T) {
	err := fmt.Errorf("failed to list documents: %w",
		&url.Error{Op: "Get", URL: "https://env.example.test", Err: context.DeadlineExceeded})

	if got := classifyGenericError(context.Background(), err); got != "error" {
		t.Errorf("CLI/serialized: classifyGenericError = %q, want %q (unchanged)", got, "error")
	}

	ctx := withInvocation(context.Background(), &invocation{concurrent: true})
	if got := classifyGenericError(ctx, err); got != "timeout" {
		t.Errorf("concurrent: classifyGenericError = %q, want %q", got, "timeout")
	}
}

// --- exitCodeForError tests ---

func TestExitCodeForError_DiagnosticError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantCode   int
	}{
		{"401 auth error", 401, client.ExitAuthError},
		{"403 permission error", 403, client.ExitPermissionError},
		{"404 not found", 404, client.ExitNotFoundError},
		{"500 server error", 500, client.ExitError},
		{"0 generic error", 0, client.ExitError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &diagnostic.Error{StatusCode: tt.statusCode}
			got := exitCodeForError(err)
			if got != tt.wantCode {
				t.Errorf("exitCodeForError() = %d, want %d", got, tt.wantCode)
			}
		})
	}
}

func TestExitCodeForError_APIError(t *testing.T) {
	err := httpclient.NewAPIError(404, "not found", "")
	got := exitCodeForError(err)
	if got != client.ExitNotFoundError {
		t.Errorf("exitCodeForError() = %d, want %d", got, client.ExitNotFoundError)
	}
}

func TestExitCodeForError_CommandError(t *testing.T) {
	err := &suggest.CommandError{Command: "geet", Message: "unknown command"}
	got := exitCodeForError(err)
	if got != client.ExitUsageError {
		t.Errorf("exitCodeForError() = %d, want %d", got, client.ExitUsageError)
	}
}

func TestExitCodeForError_FlagError(t *testing.T) {
	err := &suggest.FlagError{Flag: "outpt", Message: "unknown flag"}
	got := exitCodeForError(err)
	if got != client.ExitUsageError {
		t.Errorf("exitCodeForError() = %d, want %d", got, client.ExitUsageError)
	}
}

func TestExitCodeForError_GenericError(t *testing.T) {
	err := errors.New("generic error")
	got := exitCodeForError(err)
	if got != client.ExitError {
		t.Errorf("exitCodeForError() = %d, want %d", got, client.ExitError)
	}
}

func TestIsURLRelatedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "diagnostic 403",
			err:  &diagnostic.Error{StatusCode: 403, Message: "forbidden"},
			want: true,
		},
		{
			name: "diagnostic 401",
			err:  &diagnostic.Error{StatusCode: 401, Message: "unauthorized"},
			want: true,
		},
		{
			name: "diagnostic 404 not URL-related",
			err:  &diagnostic.Error{StatusCode: 404, Message: "not found"},
			want: false,
		},
		{
			name: "API error 403",
			err:  httpclient.NewAPIError(403, "forbidden", ""),
			want: true,
		},
		{
			name: "API error 500 not URL-related",
			err:  httpclient.NewAPIError(500, "server error", ""),
			want: false,
		},
		{
			name: "fmt.Errorf access denied",
			err:  fmt.Errorf("access denied to workflow %q", "my-wf"),
			want: true,
		},
		{
			name: "fmt.Errorf forbidden",
			err:  fmt.Errorf("forbidden: insufficient permissions"),
			want: true,
		},
		{
			name: "fmt.Errorf 403 status code",
			err:  fmt.Errorf("request failed: status 403"),
			want: true,
		},
		{
			name: "fmt.Errorf connection refused",
			err:  fmt.Errorf("connection refused"),
			want: true,
		},
		{
			name: "fmt.Errorf no such host",
			err:  fmt.Errorf("no such host"),
			want: true,
		},
		{
			name: "generic error not URL-related",
			err:  errors.New("workflow not found"),
			want: false,
		},
		{
			name: "wrapped access denied",
			err:  fmt.Errorf("failed to get workflows: %w", fmt.Errorf("access denied to workflow %q", "my-wf")),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isURLRelatedError(tt.err)
			if got != tt.want {
				t.Errorf("isURLRelatedError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTokenRefreshError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "generic error",
			err:  errors.New("workflow not found"),
			want: false,
		},
		{
			name: "compact storage refresh failure",
			err:  fmt.Errorf("failed to refresh token from compact storage: %w", fmt.Errorf("failed to refresh token: token refresh failed: 400 Bad Request - {\"error\":\"invalid_grant\"}")),
			want: true,
		},
		{
			name: "token expired and refresh failed",
			err:  fmt.Errorf("token expired and refresh failed: %w", fmt.Errorf("failed to refresh token: some error")),
			want: true,
		},
		{
			name: "simple refresh failure",
			err:  fmt.Errorf("failed to refresh token: some error"),
			want: true,
		},
		{
			name: "unrelated auth error",
			err:  fmt.Errorf("unauthorized: invalid API token"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTokenRefreshError(tt.err)
			if got != tt.want {
				t.Errorf("isTokenRefreshError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAuthHintsForError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantHints bool
	}{
		{
			name:      "nil error returns no hints",
			err:       nil,
			wantHints: false,
		},
		{
			name:      "generic error returns no hints",
			err:       errors.New("workflow not found"),
			wantHints: false,
		},
		{
			name:      "token refresh error returns login hint",
			err:       fmt.Errorf("failed to refresh token from compact storage: failed to refresh token: token refresh failed: 400 Bad Request"),
			wantHints: true,
		},
		{
			name:      "expired token returns login hint",
			err:       fmt.Errorf("token expired and refresh failed: some error"),
			wantHints: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hints := getAuthHintsForError(tt.err)
			if tt.wantHints {
				if len(hints) == 0 {
					t.Error("expected hints, got none")
				}
				// Verify the hint mentions the login command
				found := false
				for _, h := range hints {
					if strings.Contains(h, "dtctl auth login") {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected hint to mention 'dtctl auth login', got %v", hints)
				}
			} else if len(hints) > 0 {
				t.Errorf("expected no hints, got %v", hints)
			}
		})
	}
}

// TestFlagsTakingValues_SyncGuard verifies that flagsTakingValues and
// shortFlagsTakingValues are kept in sync with the PersistentFlags defined in
// init(). Every non-bool, non-count persistent flag must appear in
// flagsTakingValues (long form) and, if it has a shorthand, in
// shortFlagsTakingValues (short form). Bool/count flags must NOT appear.
func TestFlagsTakingValues_SyncGuard(t *testing.T) {
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		long := "--" + f.Name
		isBoolOrCount := f.Value.Type() == "bool" || f.Value.Type() == "count"

		if isBoolOrCount {
			if flagsTakingValues[long] {
				t.Errorf("bool/count flag %s must NOT be in flagsTakingValues", long)
			}
			if f.Shorthand != "" && shortFlagsTakingValues["-"+f.Shorthand] {
				t.Errorf("bool/count flag -%s must NOT be in shortFlagsTakingValues", f.Shorthand)
			}
		} else {
			if !flagsTakingValues[long] {
				t.Errorf("value-taking flag %s is defined in init() but missing from flagsTakingValues", long)
			}
			if f.Shorthand != "" {
				short := "-" + f.Shorthand
				if !shortFlagsTakingValues[short] {
					t.Errorf("value-taking flag %s (shorthand %s) is defined in init() but missing from shortFlagsTakingValues", long, short)
				}
			}
		}
	})

	// Reverse check: every entry in the maps must correspond to an actual flag.
	for long := range flagsTakingValues {
		name := strings.TrimPrefix(long, "--")
		if rootCmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("flagsTakingValues contains %s but no such PersistentFlag exists", long)
		}
	}
	for short := range shortFlagsTakingValues {
		letter := strings.TrimPrefix(short, "-")
		found := false
		rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Shorthand == letter {
				found = true
			}
		})
		if !found {
			t.Errorf("shortFlagsTakingValues contains %s but no PersistentFlag has that shorthand", short)
		}
	}
}

func TestValidateGlobalFlags_JQFormatConstraint(t *testing.T) {
	origOutput := outputFormat(context.Background())
	origJQ := jqFilter(context.Background())
	defer func() {
		gFlags.outputFormat = origOutput
		gFlags.jqFilter = origJQ
	}()

	tests := []struct {
		name       string
		format     string
		jq         string
		wantFormat string
	}{
		{name: "jq disabled keeps table", format: "table", jq: "", wantFormat: "table"},
		{name: "jq with json", format: "json", jq: ".records", wantFormat: "json"},
		{name: "jq with yaml", format: "yaml", jq: ".records", wantFormat: "yaml"},
		{name: "jq with yml", format: "yml", jq: ".records", wantFormat: "yml"},
		{name: "jq with toon", format: "toon", jq: ".records", wantFormat: "toon"},
		{name: "jq with table promoted", format: "table", jq: ".records", wantFormat: "json"},
		{name: "jq with csv promoted", format: "csv", jq: ".records", wantFormat: "json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.outputFormat = tt.format
			gFlags.jqFilter = tt.jq

			err := validateGlobalFlags(context.Background())
			if err != nil {
				t.Fatalf("unexpected error for format=%q jq=%q: %v", tt.format, tt.jq, err)
			}
			if outputFormat(context.Background()) != tt.wantFormat {
				t.Fatalf("gFlags.outputFormat = %q, want %q", outputFormat(context.Background()), tt.wantFormat)
			}
		})
	}
}

func TestAgentJQ_KeepContextAndFilterResult(t *testing.T) {
	origOutput := outputFormat(context.Background())
	origJQ := jqFilter(context.Background())
	origAgent := agentMode(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.outputFormat = origOutput
		gFlags.jqFilter = origJQ
		gFlags.agentMode = origAgent
		gFlags.plainMode = origPlain
	}()

	gFlags.outputFormat = "json"
	gFlags.jqFilter = ".name"
	gFlags.agentMode = true
	gFlags.plainMode = true

	var buf bytes.Buffer
	withCapturedStdout(t, &buf, func() {
		printer := NewPrinter()
		ap := enrichAgent(printer, "get", "workflow")
		if ap == nil {
			t.Fatal("expected AgentPrinter from NewPrinter in agent mode")
		}
		if err := printer.Print(map[string]interface{}{"name": "alpha", "id": 42}); err != nil {
			t.Fatalf("print failed: %v", err)
		}
	})

	var resp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected ok=true response, got: %v", resp)
	}
	if result, _ := resp["result"].(string); result != "alpha" {
		t.Fatalf("expected jq-filtered result 'alpha', got: %#v", resp["result"])
	}
	ctx, _ := resp["context"].(map[string]interface{})
	if ctx == nil {
		t.Fatalf("expected context in response, got: %v", resp)
	}
	if verb, _ := ctx["verb"].(string); verb != "get" {
		t.Fatalf("expected context.verb=get, got %q", verb)
	}
	if resource, _ := ctx["resource"].(string); resource != "workflow" {
		t.Fatalf("expected context.resource=workflow, got %q", resource)
	}
}

func TestAgentJQ_BadFilterYieldsErrorEnvelope(t *testing.T) {
	origOutput := outputFormat(context.Background())
	origJQ := jqFilter(context.Background())
	origAgent := agentMode(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.outputFormat = origOutput
		gFlags.jqFilter = origJQ
		gFlags.agentMode = origAgent
		gFlags.plainMode = origPlain
	}()

	gFlags.outputFormat = "json"
	gFlags.jqFilter = ".["
	gFlags.agentMode = true
	gFlags.plainMode = true

	var buf bytes.Buffer
	withCapturedStdout(t, &buf, func() {
		printer := NewPrinter()
		err := printer.Print(map[string]interface{}{"name": "alpha"})
		if err == nil {
			t.Fatal("expected invalid --jq filter error")
		}
		detail := errorToDetail(context.Background(), err)
		if printErr := output.PrintError(os.Stdout, detail); printErr != nil {
			t.Fatalf("failed to print error envelope: %v", printErr)
		}
	})

	var resp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected ok=false response, got: %v", resp)
	}
	errObj, _ := resp["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatalf("expected error object in response, got: %v", resp)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "invalid --jq filter") {
		t.Fatalf("expected jq error message, got: %q", msg)
	}
}

// TestAgentJQ_WrongShapeYieldsShapeMismatchEnvelope covers #413 at the envelope
// layer: a well-formed filter that addresses a key the payload doesn't have must
// come back as ok=false with the stable jq_shape_mismatch code and the payload's
// real keys in the message — not as a confident null with exit code 0.
func TestAgentJQ_WrongShapeYieldsShapeMismatchEnvelope(t *testing.T) {
	origOutput := outputFormat(context.Background())
	origJQ := jqFilter(context.Background())
	origAgent := agentMode(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.outputFormat = origOutput
		gFlags.jqFilter = origJQ
		gFlags.agentMode = origAgent
		gFlags.plainMode = origPlain
	}()

	gFlags.outputFormat = "json"
	gFlags.jqFilter = ".result.records"
	gFlags.agentMode = true
	gFlags.plainMode = true

	var buf bytes.Buffer
	withCapturedStdout(t, &buf, func() {
		printer := NewPrinter()
		err := printer.Print(map[string]interface{}{"records": []interface{}{}, "metadata": nil})
		if err == nil {
			t.Fatal("expected a shape-mismatch error for .result.records")
		}
		detail := errorToDetail(context.Background(), err)
		if printErr := output.PrintError(os.Stdout, detail); printErr != nil {
			t.Fatalf("failed to print error envelope: %v", printErr)
		}
	})

	var resp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected ok=false response, got: %v", resp)
	}
	errObj, _ := resp["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatalf("expected error object in response, got: %v", resp)
	}
	if code, _ := errObj["code"].(string); code != output.JQShapeMismatchCode {
		t.Errorf("code = %q, want %q", code, output.JQShapeMismatchCode)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "[metadata, records]") {
		t.Errorf("message does not name the payload keys: %q", msg)
	}
}

func withCapturedStdout(t *testing.T, buf *bytes.Buffer, fn func()) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	os.Stdout = w

	defer func() {
		os.Stdout = oldStdout
		_ = r.Close()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("failed to close write pipe: %v", err)
	}
	if _, err := io.Copy(buf, r); err != nil {
		t.Fatalf("failed to read captured stdout: %v", err)
	}
}

// TestNounAdvice pins that `dtctl <noun>` names the commands reading it, not an edit-distance guess.
func TestNounAdvice(t *testing.T) {
	cases := []struct {
		noun string
		want []string
	}{
		{"slo", []string{"dtctl get slos"}},
		{"workflow", []string{"dtctl get workflows", "dtctl get workflow-executions"}},
		{"problems", []string{"fetch dt.davis.problems"}},
		{"synthetic", []string{"fetch dt.synthetic.events"}},
		{"slo-status", []string{"dtctl get slos"}},
		{"workflow-runs", []string{"dtctl get workflows", "dtctl get workflow-executions"}},
	}
	for _, tc := range cases {
		err := enhanceCommandError(rootCmd, fmt.Errorf(`unknown command %q for "dtctl"`, tc.noun))
		var cmdErr *suggest.CommandError
		if !errors.As(err, &cmdErr) {
			t.Fatalf("%s: expected CommandError, got %T", tc.noun, err)
		}
		if cmdErr.Suggestion != nil {
			t.Errorf("%s: kept the edit-distance guess %q", tc.noun, cmdErr.Suggestion.Value)
		}
		got := strings.Join(errorToDetail(context.Background(), err).Suggestions, "\n")
		for _, w := range append(tc.want, "dtctl commands") {
			if !strings.Contains(got, w) {
				t.Errorf("%s: suggestions lack %q:\n%s", tc.noun, w, got)
			}
		}
	}
	// The same advice under a verb: `dtctl get problems`, `dtctl find slo`.
	for noun, want := range map[string]string{"problems": "fetch dt.davis.problems", "slo": "dtctl get slos"} {
		err := requireSubcommand(findCmd, []string{noun})
		if got := strings.Join(errorToDetail(context.Background(), err).Suggestions, "\n"); !strings.Contains(got, want) {
			t.Errorf("find %s: suggestions lack %q:\n%s", noun, want, got)
		}
	}
	for _, typo := range []string{"quer", "getx", "account"} {
		if a := nounAdvice(rootCmd, typo); a != nil {
			t.Errorf("%s is no noun dtctl reads, got %q", typo, a)
		}
	}
}
