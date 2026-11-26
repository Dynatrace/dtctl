# AI Agent Skills

<!-- Migrated from the standalone docs site; SME to verify. -->

AI agent skills are portable knowledge packages that give coding agents the
context they need to work with Dynatrace. dtctl ships with its own skill and
integrates with the broader
[Dynatrace for AI](https://github.com/Dynatrace/dynatrace-for-ai) skill
collection.

For install commands (`npx skills add`, `dtctl skills install`, manual copy),
see the "AI Agent Skills" section of the [README](../README.md). Check what is
installed with `dtctl skills status`, and remove it with `dtctl skills uninstall`.

## What is a skill?

Skills follow the [Agent Skills](https://agentskills.io) open format. They are
small, structured files that teach AI coding agents about a specific domain or
tool. Agents load only what they need: a short description for discovery, full
instructions when relevant, and detailed reference files on demand.

Skills work with Claude Code, GitHub Copilot, Cursor, Kiro, Junie, OpenCode,
OpenClaw, Gemini CLI, and many other compatible tools.

dtctl includes a built-in skill at `skills/dtctl/` that teaches AI agents how
to operate dtctl: which commands to run, what flags to use, how to read
output, and how to chain operations together.

## Dynatrace domain skills

The dtctl skill teaches agents how to use the CLI tool. For deeper Dynatrace
domain knowledge, install the skills from
[Dynatrace/dynatrace-for-ai](https://github.com/Dynatrace/dynatrace-for-ai).

These skills cover:

| Category | Skills |
|----------|--------|
| **DQL & Query Language** | DQL syntax rules, common pitfalls, query patterns |
| **Observability** | Services, frontends, distributed tracing, hosts, Kubernetes, AWS, logs, problems |
| **Platform** | Dashboards, notebooks |
| **Migration** | Classic entity-based DQL to Smartscape equivalents |

The domain skills provide context (how to write DQL queries, which metrics to
use for service health, how to navigate distributed traces) while the dtctl
skill provides the operational tool to act on it. Together they give AI agents
everything they need to work with Dynatrace effectively.

You can install all skills without penalty. Agents use progressive disclosure
and only load what they need for the current task.

The dtctl skill points agents at these skills, and `dtctl skills install`
prints a hint with the install command when `dt-dql-essentials` is not found
in the same agent's skill directories (in agent mode, the hint goes to
`context.suggestions` instead of stderr):

```bash
npx skills add dynatrace/dynatrace-for-ai
```
