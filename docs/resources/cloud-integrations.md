<!-- Migrated from the standalone docs site; SME to verify against the current dtctl binary. -->

# Cloud Integrations

[dtctl docs](../README.md) / [Resources](README.md) / Cloud Integrations

<!-- Verified against the dtctl binary on 2026-09-18. Hand-authored: the aws/azure/gcp commands are nested subcommands (verb -> provider -> noun) that `commands --full` does not yet expose, so these tables are NOT generated. Re-verify when the cloud command surface changes; move into GENERATED markers once `commands --full` exposes resource_details (per code-owner review). -->

## Overview

dtctl supports configuring cloud monitoring integrations for AWS, Azure, and GCP (GCP support is currently in Preview). Each integration follows a connection-then-configuration pattern: first establish a connection with credentials, then create a monitoring configuration that defines what to monitor. Monitoring configurations are always created in a **disabled** state; a separate `dtctl enable` step activates them.

## Supported operations

There is no dedicated `aws`/`azure`/`gcp` resource type in the command catalog. These operations are exposed as their own verb + noun-phrase pairs (`create aws connection`, `create azure monitoring`, `enable gcp monitoring`, ...) layered over the Settings API, rather than generic `get`/`create`/`delete` on a single resource name.

| Operation | Resource | Command syntax | Description | Mutating | Access |
| --- | --- | --- | --- | --- | --- |
| create | aws connection | `dtctl create aws connection` | Create AWS connection from flags | yes | write |
| create | aws monitoring | `dtctl create aws monitoring` | Create AWS monitoring config from flags | yes | write |
| get | aws connections | `dtctl get aws connections` | Get AWS connections | no | read |
| get | aws monitoring | `dtctl get aws monitoring` | Get AWS monitoring configurations | no | read |
| get | aws monitoring-feature-sets | `dtctl get aws monitoring-feature-sets` | Get available AWS monitoring config feature sets | no | read |
| get | aws monitoring-regions | `dtctl get aws monitoring-regions` | Get available AWS monitoring config regions | no | read |
| describe | aws connection | `dtctl describe aws connection` | Show details of an AWS connection | no | read |
| describe | aws monitoring | `dtctl describe aws monitoring` | Show details of an AWS monitoring configuration | no | read |
| delete | aws connection | `dtctl delete aws connection` | Delete an AWS connection | yes | delete |
| delete | aws monitoring | `dtctl delete aws monitoring` | Delete an AWS monitoring config | yes | delete |
| update | aws connection | `dtctl update aws connection` | Update AWS connection from flags | yes | write |
| update | aws monitoring | `dtctl update aws monitoring` | Update AWS monitoring config from flags | yes | write |
| enable | aws monitoring | `dtctl enable aws monitoring` | Enable AWS monitoring configuration | yes | write |
| disable | aws monitoring | `dtctl disable aws monitoring` | Disable AWS monitoring configuration | yes | write |
| apply | aws extension-config | `dtctl apply aws extension-config` | Apply a monitoring configuration for an extension | yes | write |
| create | azure connection | `dtctl create azure connection` | Create Azure connection from flags | yes | write |
| create | azure monitoring | `dtctl create azure monitoring` | Create Azure monitoring config from flags | yes | write |
| get | azure connections | `dtctl get azure connections` | Get Azure connections | no | read |
| get | azure monitoring | `dtctl get azure monitoring` | Get Azure monitoring configurations | no | read |
| get | azure monitoring-feature-sets | `dtctl get azure monitoring-feature-sets` | Get available Azure monitoring config feature sets | no | read |
| get | azure monitoring-locations | `dtctl get azure monitoring-locations` | Get available Azure monitoring config locations | no | read |
| describe | azure connection | `dtctl describe azure connection` | Show details of an Azure connection (credential) | no | read |
| describe | azure monitoring | `dtctl describe azure monitoring` | Show details of an Azure monitoring configuration | no | read |
| delete | azure connection | `dtctl delete azure connection` | Delete an Azure connection | yes | delete |
| delete | azure monitoring | `dtctl delete azure monitoring` | Delete an Azure monitoring config | yes | delete |
| update | azure connection | `dtctl update azure connection` | Update Azure connection from flags | yes | write |
| update | azure monitoring | `dtctl update azure monitoring` | Update Azure monitoring config from flags | yes | write |
| enable | azure monitoring | `dtctl enable azure monitoring` | Enable Azure monitoring configuration | yes | write |
| disable | azure monitoring | `dtctl disable azure monitoring` | Disable Azure monitoring configuration | yes | write |
| apply | azure extension-config | `dtctl apply azure extension-config` | Apply a monitoring configuration for an extension | yes | write |
| create | gcp connection | `dtctl create gcp connection` | Create GCP connection from flags | yes | write |
| create | gcp monitoring | `dtctl create gcp monitoring` | Create GCP monitoring config from flags | yes | write |
| get | gcp connections | `dtctl get gcp connections` | Get GCP connections | no | read |
| get | gcp monitoring | `dtctl get gcp monitoring` | Get GCP monitoring configurations | no | read |
| get | gcp monitoring-feature-sets | `dtctl get gcp monitoring-feature-sets` | Get available GCP monitoring config feature sets | no | read |
| get | gcp monitoring-locations | `dtctl get gcp monitoring-locations` | Get available GCP monitoring config locations | no | read |
| describe | gcp connection | `dtctl describe gcp connection` | Show details of a GCP connection | no | read |
| describe | gcp monitoring | `dtctl describe gcp monitoring` | Show details of a GCP monitoring configuration | no | read |
| delete | gcp connection | `dtctl delete gcp connection` | Delete a GCP connection | yes | delete |
| delete | gcp monitoring | `dtctl delete gcp monitoring` | Delete a GCP monitoring config | yes | delete |
| update | gcp connection | `dtctl update gcp connection` | Update GCP connection from flags | yes | write |
| update | gcp monitoring | `dtctl update gcp monitoring` | Update GCP monitoring config from flags | yes | write |
| enable | gcp monitoring | `dtctl enable gcp monitoring` | Enable GCP monitoring configuration | yes | write |
| disable | gcp monitoring | `dtctl disable gcp monitoring` | Disable GCP monitoring configuration | yes | write |
| apply | gcp extension-config | `dtctl apply gcp extension-config` | Apply a monitoring configuration for an extension | yes | write |

## Flags

Resource commands take dtctl's **global flags** (`-o/--output`, `--context`, `--jq`, `-v`, ...). Cloud-integration commands add their own noun-specific flags, such as `--name`, `--regions`, `--featureSets`, `--credentials`, `--roleArn` (AWS), `--type`, `--directoryId`, `--applicationId`, `--clientSecret`, `--locationFiltering` (Azure), and `--serviceAccountId` (GCP connections), plus `--credentials`, `--locationFiltering`, and `--featureSets` (GCP monitoring). `create aws|azure|gcp monitoring` also takes `--central-enrichment` (see [Central enrichment](#central-enrichment)). See the examples below for their usage in context.

## Required token scopes

Cloud integration connections and monitoring configurations are managed through the Settings API and, for the underlying extension configuration, the Extensions API:

| Safety level | Scopes |
| --- | --- |
| read | `settings:objects:read`, `extensions:configurations:read` |
| write | `settings:objects:write`, `extensions:configurations:write` |

## Output

`create`/`update` commands print the created or updated object's identifying fields; `create aws connection` and `create azure connection` additionally print copy-paste setup material (a CloudFormation snippet for AWS; Issuer/Subject/Audiences values for Azure federated identity). `get aws monitoring-regions`, `get aws monitoring-feature-sets`, `get gcp monitoring-locations`, and `get gcp monitoring-feature-sets` print the discoverable values needed for the `--regions`, `--featureSets`, and `--locationFiltering` flags elsewhere in the workflow.

## Examples

### AWS monitoring

AWS monitoring uses role-based authentication: the connection's `objectId` becomes `sts:ExternalId` in the IAM role's trust policy, provisioned via a Dynatrace-maintained CloudFormation template.

```bash
# Step 1: create the connection first — the role ARN is patched in later
dtctl create aws connection --name "my-aws-connection"
```

The command prints a copy-paste `aws cloudformation deploy` snippet that creates the IAM role using the connection's `objectId` as `sts:ExternalId`. Run it in AWS CloudShell, then patch the resulting role ARN into the connection:

```bash
dtctl update aws connection --name "my-aws-connection" --roleArn "$ROLE_ARN"
```

Create a monitoring configuration (`--regions` is required; `--featureSets` is optional and defaults to the extension's default set). Monitoring configurations are created disabled:

```bash
dtctl create aws monitoring --name "my-aws-monitoring" \
  --credentials "my-aws-connection" \
  --regions us-east-1,eu-central-1
```

Discover regions and feature sets, then enable:

```bash
dtctl get aws monitoring-regions
dtctl get aws monitoring-feature-sets

dtctl enable aws monitoring --name "my-aws-monitoring"

# Or patch the role ARN and enable in one step:
dtctl enable aws monitoring --name "my-aws-monitoring" \
  --roleArn arn:aws:iam::123456789012:role/DynatraceMonitoringRole
```

Update and delete:

```bash
dtctl update aws monitoring --name "my-aws-monitoring" \
  --regions us-east-1,eu-central-1,ap-southeast-2 \
  --featureSets EC2_essential,RDS_essential

dtctl delete aws monitoring my-aws-monitoring
dtctl delete aws connection my-aws-connection
```

### Azure monitoring

dtctl supports two Azure authentication types: `federatedIdentityCredential` (recommended, no long-lived secrets) and `clientSecret` (service principal with a password).

```bash
# Create the service principal and assign the Reader role
CLIENT_ID=$(az ad sp create-for-rbac --name "$CONNECTION_NAME" --create-password false --query appId -o tsv)
az role assignment create --assignee "$CLIENT_ID" --role Reader --scope "/subscriptions/$SUBSCRIPTION_ID"

# Create the connection — its ID becomes the federated credential subject
dtctl create azure connection --name "$CONNECTION_NAME" --type federatedIdentityCredential
```

`dtctl create azure connection` prints the Issuer, Subject, and Audiences values (and a ready-to-run `az` command) to add as a federated credential in Entra ID. Then finalize:

```bash
dtctl update azure connection \
  --name "$CONNECTION_NAME" \
  --directoryId "$TENANT_ID" \
  --applicationId "$CLIENT_ID"
```

For the `clientSecret` type, create the connection with the secret directly:

```bash
dtctl create azure connection \
  --name "$CONNECTION_NAME" \
  --type clientSecret \
  --directoryId "$TENANT_ID" \
  --applicationId "$CLIENT_ID" \
  --clientSecret "$CLIENT_SECRET"
```

Create a monitoring configuration (created disabled), optionally scoped to regions/feature sets, then enable:

```bash
dtctl create azure monitoring \
  --name "$CONNECTION_NAME" \
  --credentials "$CONNECTION_NAME" \
  --locationFiltering westeurope,northeurope \
  --featureSets microsoft_compute.virtualmachines_essential

dtctl enable azure monitoring --name "$CONNECTION_NAME"
```

Rotate an expired client secret with `--append`, so the old secret stays valid until you cut over:

```bash
NEW_SECRET=$(az ad app credential reset --id "$CLIENT_ID" --append \
  --display-name "dtctl-$(date +%Y-%m-%d)" --query password -o tsv)

dtctl update azure connection --name "$CONNECTION_NAME" --clientSecret "$NEW_SECRET"
```

Update and delete:

```bash
dtctl update azure monitoring "$CONNECTION_NAME" \
  --locationFiltering westeurope,northeurope \
  --featureSets microsoft_compute.virtualmachines_essential

dtctl delete azure monitoring "$CONNECTION_NAME"
dtctl delete azure connection "$CONNECTION_NAME"
```

### GCP monitoring (Preview)

```bash
# Create the connection, then set up a GCP service account with gcloud
dtctl create gcp connection --name "my-gcp-connection"

gcloud iam service-accounts create dynatrace-monitoring --display-name "Dynatrace Monitoring"
gcloud projects add-iam-policy-binding <project-id> \
  --member "serviceAccount:dynatrace-monitoring@<project-id>.iam.gserviceaccount.com" \
  --role "roles/monitoring.viewer"

# Set the service account email on the connection
dtctl update gcp connection \
  --name "my-gcp-connection" \
  --serviceAccountId "dynatrace-monitoring@<project-id>.iam.gserviceaccount.com"

# Create a monitoring configuration against the connection (created disabled).
# Without --locationFiltering it monitors every location (no location filter).
dtctl create gcp monitoring --name "my-gcp-monitoring" --credentials "my-gcp-connection"

# Discover locations and feature sets
dtctl get gcp monitoring-locations
dtctl get gcp monitoring-feature-sets

# Enable
dtctl enable gcp monitoring --name "my-gcp-monitoring"
```

Update and delete:

```bash
dtctl update gcp monitoring --name "my-gcp-monitoring" \
  --locationFiltering us-central1,europe-west1

# Remove the location filter again (monitor every location)
dtctl update gcp monitoring --name "my-gcp-monitoring" --locationFiltering all

dtctl delete gcp monitoring my-gcp-monitoring
dtctl delete gcp connection my-gcp-connection
```

## Notes

### Central enrichment

`dtctl create aws|azure|gcp monitoring` creates the configuration with central enrichment (`useIngestEnrichmentConfig: true`). Pass `--central-enrichment=false` to keep legacy enrichment. Some environments enforce central enrichment on every write, and there the stored configuration is central regardless of the flag. `update` and `enable` resend the stored value, and `apply` sends `useIngestEnrichmentConfig` only if the file sets it.

### AWS: IAM role deployment script

The connection command prints a ready-to-run snippet, but the underlying script is worth knowing if you need to adapt it (for example, to change the stack name or region). It downloads Dynatrace's least-privilege role template and deploys it as a CloudFormation stack, then reads the role ARN back out of the stack outputs:

```bash
STACK="dynatrace-monitoring-my-aws-connection"
curl -fsSLo da-role.yaml https://dynatrace-data-acquisition.s3.amazonaws.com/aws/deployment/cfn/latest/da-aws-nested-monitoring-role.yaml
aws cloudformation deploy \
  --stack-name "$STACK" \
  --template-file da-role.yaml \
  --parameter-overrides pDynatraceUrl=<your-tenant-url> pRoleExternalId=<connection-object-id> \
  --capabilities CAPABILITY_NAMED_IAM

ROLE_ARN=$(aws cloudformation describe-stacks --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='DynatraceMonitoringRoleArn'].OutputValue" --output text)
```

Run this in AWS CloudShell. The stack name follows the pattern `dynatrace-monitoring-<connection-name>`, which keeps it identifiable if you manage multiple AWS connections. `pRoleExternalId` must be the connection's `objectId`, and `pDynatraceUrl` is your tenant URL. `CAPABILITY_NAMED_IAM` is required because the template creates a named IAM role.

### Azure: choosing and naming the subscription

Before creating an Azure connection, pick the subscription you want to monitor and derive a connection name from it. Naming the connection after the subscription keeps multiple connections easy to tell apart:

```bash
az account list --output table

SUBSCRIPTION_ID=$(az account show --query id -o tsv)
SUBSCRIPTION_NAME=$(az account show --query name -o tsv)
TENANT_ID=$(az account show --query tenantId -o tsv)

# Subscription names can contain spaces. Normalize them to dashes.
CONNECTION_NAME="dtctl-$(echo "$SUBSCRIPTION_NAME" | tr ' ' '-')"
```

### Multiple subscriptions

Both Azure authentication types (federated identity and client secret) use a single service principal that can be granted the Reader role on more than one subscription. Repeat the `az role assignment create` command, once per additional subscription, against the same `$CLIENT_ID`, rather than creating a separate service principal for each one.

### Azure: security considerations for client secrets

`az ad sp create-for-rbac` prints the client secret only once, so capture it immediately when creating a service principal for the `clientSecret` authentication type.

When you pass `--clientSecret` to `dtctl`, the value doesn't touch bash history or disk, but it can still be visible in the `dtctl` process arguments while the command runs. Avoid running this on shared machines, and be aware of any process-argument logging in your environment.

### GCP: workload identity federation setup

After creating the service account and granting it the monitoring viewer role, you still need to configure workload identity federation or impersonation so that Dynatrace can assume the service account. Run `dtctl describe gcp connection` after creating the connection; it prints the specific instructions for this step.
