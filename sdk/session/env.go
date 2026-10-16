package session

import "os"

// WithEnv makes the Config consult lookup, rather than the process
// environment, for the variables its methods read: the profile, the stability
// floor, development features and the deprecation mode. An embedder that runs
// several invocations in one process, each with an environment of its own,
// gives each its own Config and its own lookup. nil restores the process
// environment.
//
// The lookup answers os.LookupEnv's question: the value and whether the
// variable is set at all. It is not consulted for the package-level
// configuration loaders and keyring probes, which read the process
// environment; a sealed Config (SealInlineCredentials) never reaches them.
func (c *Config) WithEnv(lookup func(key string) (string, bool)) *Config {
	c.env = lookup
	return c
}

// lookupEnv is os.LookupEnv through the Config's lookup.
func (c *Config) lookupEnv(key string) (string, bool) {
	if c.env != nil {
		return c.env(key)
	}
	return os.LookupEnv(key)
}

// getenv is os.Getenv through the Config's lookup.
func (c *Config) getenv(key string) string {
	v, _ := c.lookupEnv(key)
	return v
}
