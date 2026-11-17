# Integration Tests

This directory contains end-to-end integration tests for dtctl that run against a real Dynatrace environment.

## Overview

The integration tests verify the complete lifecycle of dtctl operations against actual Dynatrace APIs:

- **Workflows**: Create, get, list, update, execute, history, restore, delete
- **Dashboards**: Create, get, list, update, snapshots, restore, delete
- **Notebooks**: Create, get, list, update, snapshots, restore, delete
- **Grail Buckets**: Create, get, list, update, delete
- **Settings**: Create, get, list, update, delete, validate, schemas
- **SLOs**: Create, get, list, update, delete, evaluate, templates
- **EdgeConnect**: Create, get, list, update, delete

## Prerequisites

### Required Environment Variables

The integration tests require two environment variables to connect to your Dynatrace environment:

```bash
export DTCTL_INTEGRATION_ENV="https://your-env-id.apps.dynatrace.com"
export DTCTL_INTEGRATION_TOKEN="dt0s16.YOUR_PLATFORM_TOKEN"
```

### Required Token Scopes

Your platform token needs scopes for:
- Workflows: `automation:workflows:*`
- Documents: `document:documents:*`
- Storage: `storage:bucket-definitions:read`, `storage:bucket-definitions:write`, `storage:bucket-definitions:delete`, `storage:buckets:read`, `storage:logs:read`
- Settings: `settings:objects:*`, `settings:schemas:read`
- SLOs: `slo:*`
- EdgeConnect: `app-engine:edge-connects:*`

See [../../docs/TOKEN_SCOPES.md](../../docs/TOKEN_SCOPES.md) for complete scope reference.

### Creating a Platform Token

1. Navigate to your Dynatrace environment
2. Go to **Access Tokens** (Settings → Access tokens)
3. Click **Generate new token**
4. Select the required scopes listed above
5. Copy the token and set it as `DTCTL_INTEGRATION_TOKEN`

## Running the Tests

### Run All Integration Tests

```bash
DTCTL_INTEGRATION_ENV="https://your-env.apps.dynatrace.com" \
DTCTL_INTEGRATION_TOKEN="dt0s16.YOUR_TOKEN" \
make test-integration
```

### Run Specific Test Files

```bash
# Workflows only
go test -v -race -tags integration ./test/e2e/workflow_test.go

# Dashboards only
go test -v -race -tags integration ./test/e2e/dashboard_test.go

# Notebooks only
go test -v -race -tags integration ./test/e2e/notebook_test.go

# Buckets only
go test -v -race -tags integration ./test/e2e/bucket_test.go

# Settings only
go test -v -race -tags integration ./test/e2e/settings_test.go

# SLOs only
go test -v -race -tags integration ./test/e2e/slo_test.go

# EdgeConnect only
go test -v -race -tags integration ./test/e2e/edgeconnect_test.go
```

### Run Specific Test Functions

```bash
# Workflow tests
go test -v -race -tags integration -run TestWorkflowLifecycle ./test/e2e/

# Dashboard tests
go test -v -race -tags integration -run TestDashboardLifecycle ./test/e2e/

# Settings tests
go test -v -race -tags integration -run TestSettingsLifecycle ./test/e2e/

# SLO tests
go test -v -race -tags integration -run TestSLOLifecycle ./test/e2e/

# EdgeConnect tests
go test -v -race -tags integration -run TestEdgeConnectLifecycle ./test/e2e/
```

## How Tests Work

### Test Isolation

- Each test run generates a unique prefix: `dtctl-test-{timestamp}-{random}`
- All created resources use this prefix to avoid conflicts
- Tests can safely run in parallel or concurrently

### Automatic Cleanup

- Tests track all created resources using a `CleanupTracker`
- Resources are deleted in reverse order (LIFO) during cleanup
- Cleanup runs even if tests fail (via `defer`)
- Deletion is verified (GET after DELETE should return 404)

### Example Test Flow (Workflow)

```
1. Create workflow with unique name
2. Verify creation (GET by ID)
3. List workflows (verify test workflow appears)
4. Update workflow content
5. Check version history (should have 2+ versions)
6. Execute workflow
7. Wait for execution completion
8. Restore to previous version
9. Delete workflow
10. Verify deletion (expect 404)
```

## Safety Features

### No Destructive Operations

- Tests **only** create, modify, and delete resources they created
- Never touch existing resources in your environment
- Unique naming prevents accidental conflicts

### Environment Variable Gating

- Tests skip automatically if `DTCTL_INTEGRATION_ENV` or `DTCTL_INTEGRATION_TOKEN` are not set
- No accidental test runs against production

### Cleanup Verification

- After deleting a resource, tests verify it's actually gone
- If cleanup fails, tests log errors (helps identify orphaned resources)

## Troubleshooting

### Tests Are Skipped

**Problem**: Tests show "SKIP" messages

**Solution**: Ensure both environment variables are set:
```bash
echo $DTCTL_INTEGRATION_ENV
echo $DTCTL_INTEGRATION_TOKEN
```

### Permission Errors

**Problem**: Tests fail with "403 Access Denied" errors

**Solution**: Verify your token has all required scopes (see Prerequisites)

### Resources Left Behind

**Problem**: Test resources remain after tests complete

**Solution**:
1. Check test output for cleanup errors
2. Manually clean up using dtctl:
   ```bash
   # List resources with test prefix
   dtctl get workflows | grep dtctl-test-
   dtctl get dashboards | grep dtctl-test-

   # Delete manually if needed
   dtctl delete workflow <workflow-id>
   dtctl delete dashboard <dashboard-id>
   ```

### Execution Timeouts

**Problem**: Workflow execution tests timeout

**Solution**: This is expected for long-running workflows. Tests log a warning but don't fail.

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Integration Tests
on: [push, pull_request]

jobs:
  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.24'

      - name: Run Integration Tests
        env:
          DTCTL_INTEGRATION_ENV: ${{ secrets.DYNATRACE_ENV_URL }}
          DTCTL_INTEGRATION_TOKEN: ${{ secrets.DYNATRACE_TOKEN }}
        run: make test-integration
```

## Development

### Adding New Integration Tests

1. Create test file in `test/e2e/` with build tags:
   ```go
   //go:build integration
   // +build integration

   package e2e
   ```

2. Use `integration.SetupIntegration(t)` to initialize environment

3. Track created resources:
   ```go
   env.Cleanup.Track("resource-type", id, name)
   ```

4. Follow table-driven test pattern (see existing tests)

### Test Utilities

Integration test helpers are in `test/integration/`:

- `setup.go`: Environment initialization
- `cleanup.go`: Resource cleanup tracking
- `fixtures.go`: Test resource builders

## Architecture

```
test/
├── integration/           # Shared test infrastructure
│   ├── setup.go          # SetupIntegration(), test prefix generation
│   ├── cleanup.go        # CleanupTracker for resource cleanup
│   └── fixtures.go       # Resource fixture builders
└── e2e/                  # Actual integration tests
    ├── workflow_test.go     # Workflow lifecycle tests
    ├── dashboard_test.go    # Dashboard lifecycle tests
    ├── notebook_test.go     # Notebook lifecycle tests
    ├── bucket_test.go       # Bucket lifecycle tests
    ├── settings_test.go     # Settings lifecycle tests
    ├── slo_test.go         # SLO lifecycle and evaluation tests
    ├── edgeconnect_test.go  # EdgeConnect lifecycle tests
    ├── README.md           # This file
    └── TESTING_STATUS.md   # Test status and coverage details
```

## Best Practices

1. **Always use `defer env.Cleanup.Cleanup(t)`** - Ensures cleanup runs even on failures
2. **Track all created resources** - Use `env.Cleanup.Track()` immediately after creation
3. **Verify cleanup** - Check that DELETE actually worked (expect 404)
4. **Use unique prefixes** - Leverage `env.TestPrefix` in all resource names
5. **Test error cases** - Include tests for invalid inputs and edge cases
6. **Don't assume success** - Workflow executions may fail for various reasons

## Example Output

```
=== RUN   TestWorkflowLifecycle
=== RUN   TestWorkflowLifecycle/complete_workflow_lifecycle
    workflow_test.go:31: Integration test environment initialized with prefix: dtctl-test-1704657890-a3c5f2
    workflow_test.go:36: Step 1: Creating workflow...
    workflow_test.go:47: ✓ Created workflow: dtctl-test-1704657890-a3c5f2-workflow (ID: wf-12345)
    workflow_test.go:52: Step 2: Getting workflow...
    workflow_test.go:63: ✓ Retrieved workflow: dtctl-test-1704657890-a3c5f2-workflow
    workflow_test.go:66: Step 3: Listing workflows...
    workflow_test.go:78: ✓ Found workflow in list (total: 15 workflows)
    workflow_test.go:81: Step 4: Updating workflow...
    workflow_test.go:90: ✓ Updated workflow: dtctl-test-1704657890-a3c5f2-workflow → dtctl-test-1704657890-a3c5f2-workflow-modified
    workflow_test.go:93: Step 5: Checking version history...
    workflow_test.go:102: ✓ Version history contains 2 versions
    workflow_test.go:117: Step 7: Executing workflow...
    workflow_test.go:129: ✓ Started execution: exec-abc123 (state: RUNNING)
    workflow_test.go:132: Step 8: Waiting for execution to complete...
    workflow_test.go:144: ✓ Execution completed with state: SUCCESS
    workflow_test.go:164: Step 10: Deleting workflow...
    workflow_test.go:169: ✓ Deleted workflow: wf-12345
    workflow_test.go:172: Step 11: Verifying deletion...
    workflow_test.go:177: ✓ Verified deletion (got expected error: failed to get workflow: status 404)
    cleanup.go:52: Cleaning up workflow: dtctl-test-1704657890-a3c5f2-workflow (ID: wf-12345)
    cleanup.go:67: Successfully cleaned up and verified deletion of workflow: dtctl-test-1704657890-a3c5f2-workflow
--- PASS: TestWorkflowLifecycle (18.34s)
    --- PASS: TestWorkflowLifecycle/complete_workflow_lifecycle (18.34s)
PASS
```
