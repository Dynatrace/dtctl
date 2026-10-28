package aidetect

import "github.com/Dynatrace/dtctl/sdk/agentmode"

// AgentInfo represents detected AI agent information.
// Alias for agentmode.AgentInfo from the SDK.
type AgentInfo = agentmode.AgentInfo

// Detect checks environment variables to identify if running under an AI agent.
// Delegates to agentmode.Detect from the SDK.
func Detect() AgentInfo {
	return agentmode.Detect()
}

// EnvVars returns every environment variable Detect consults.
// Delegates to agentmode.EnvVars from the SDK.
func EnvVars() []string {
	return agentmode.EnvVars()
}

// UserAgentSuffix returns a suffix to append to the User-Agent header.
// Delegates to agentmode.UserAgentSuffix from the SDK.
func UserAgentSuffix() string {
	return agentmode.UserAgentSuffix()
}
