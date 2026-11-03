# gardenlinux-integration-test

## Overview

`gardenlinux-integration-test` is a Go CLI tool that acts as the **bridge between a Garden Linux release and the Gardener integration test workflow**. Its job is to prepare everything a downstream release workflow needs to spin up real Shoot clusters and validate a Garden Linux release before it is merged into Gardener.

## What the tool does

The tool exposes two sub-commands:

**`publish`** — given a release `--version` and `--commit`:

1. **Downloads release manifests** from S3 (`gardenlinux-github-releases`) for every supported cloud platform and architecture combination (AWS amd64/arm64, Alicloud amd64, GCP amd64/arm64, OpenStack amd64, Azure amd64/arm64). AWS credentials to reach S3 are obtained on-the-fly from HashiCorp Vault (`vault.tools.sap`, namespace `gardnlinux`).
2. **Builds a `NamespacedCloudProfile`** (a Gardener CRD) for each cloud provider. The profile is scoped to the `garden-gl-it` namespace and carries the provider-specific machine image metadata (AMI IDs for AWS, image IDs for Alicloud/OpenStack, GCP project/image paths, Azure community gallery IDs) extracted from the manifests.
3. **Builds a `Shoot` spec** for each cloud profile, hard-coding sensible defaults per provider: an EU region, worker machine types (including architecture-specific types like `a1.2xlarge` for AWS arm64 or `t2a-standard-2` for GCP arm64), network CIDRs, and Kubernetes version.
4. **Uploads both the `NamespacedCloudProfile` YAML and the `Shoot` YAML** to a separate S3 bucket (`gardenlinux-test-import`) under structured keys:
   - `meta/NSCloudProfile/<version>/<profile-name>-<short-commit>`
   - `meta/ShootSpec/<version>/<profile-name>-<short-commit>`

   The downstream release workflow reads these objects to create the actual Shoot cluster in a Gardener instance and run integration tests against it.

**`unpublish`** — performs the reverse: re-derives the same S3 keys from the same inputs and deletes them, cleaning up after a release test cycle.

## Automated Integration Test Flow

Testing a Garden Linux release is a **two-step process** that runs automatically after a successful GLCI build:

### Step 1 — Upload specs (this repo)

Once GLCI completes, the **Integration Test workflow** in this repository is triggered (should be added to Release process). It:

1. Authenticates to Vault using GitHub OIDC to obtain short-lived AWS credentials.
2. Runs `publish -v <version> -c <commit>` to download the release manifests and upload the `NamespacedCloudProfile` and `Shoot` specs to S3.

### Step 2 — Authenticate and test (release-workflow repo)

After the specs are in S3, the **authentication and testing workflow** in [`gardenlinux/release-workflow`](https://github.tools.sap/gardenlinux/release-workflow) (currently on the `feat/IntegrationTest` branch) is triggered. It:

1. Authenticates to the Gardener cluster via Vault.
2. Downloads the `NamespacedCloudProfile` and `Shoot` YAML from S3.
3. Applies them to the `garden-gl-it` namespace.
4. Waits for the Shoot cluster to become ready.
5. Runs Kubernetes Node Conformance tests against the Shoot.
6. Cleans up the Shoot and `NamespacedCloudProfile` after the test.

```
GLCI completes
      │
      ▼
[Step 1] gardenlinux-integration-test
  publish workflow → uploads NSCloudProfile + ShootSpec to S3
      │
      ▼
[Step 2] gardenlinux/release-workflow (feat/IntegrationTest)
  download → apply → wait → test → cleanup
```

## Usage (If spec needs to be updated manually)

To publish or unpublish integration test resources for a Garden Linux release, you need two pieces of information:

1. **Version** — the Garden Linux release version (e.g. `2150.12.0`)
2. **Commit ID** — the release commitish (e.g. the full or short Git SHA)

### Publish

Builds and uploads the `NamespacedCloudProfile` and `Shoot` specs for all cloud providers:

```bash
export VAULT_TOKEN=<your-vault-token>

go run . publish -v 2150.12.0 -c <commit-id>
```

Example:

```bash
go run . publish -v 2150.12.0 -c abc12345
```

### Unpublish

Removes the previously uploaded specs from S3:

```bash
go run . unpublish -v 2150.12.0 -c <commit-id>
```

### Prerequisites

- `VAULT_TOKEN` environment variable must be set with a valid token that has access to the `gardnlinux` namespace on `vault.tools.sap`.
- Network access to `vault.tools.sap` and the `gardenlinux-github-releases` / `gardenlinux-test-import` S3 buckets.

## Background & References

### NamespacedCloudProfile

A `NamespacedCloudProfile` is a Gardener CRD that extends a parent `CloudProfile` with additional machine images, scoped to a single namespace (project). It avoids modifying the shared cluster-wide `CloudProfile` while still allowing a project to test pre-release images.

**Example for Garden Linux 1877.15 on AWS** :

```yaml
apiVersion: core.gardener.cloud/v1beta1
kind: NamespacedCloudProfile
metadata:
  name: aws-gardenlinux-1877
  namespace: garden-<project-id>
spec:
  parent:
    kind: CloudProfile
    name: aws
  machineImages:
  - name: gardenlinux
    versions:
    - version: "1877.15.0"
      architectures:
      - amd64
      - arm64
      cri:
      - name: containerd
  providerConfig:
    apiVersion: aws.provider.extensions.gardener.cloud/v1alpha1
    kind: CloudProfileConfig
    machineImages:
    - name: gardenlinux
      versions:
      - version: "1877.15.0"
        regions:
        - name: eu-central-1
          ami: ami-07ed85dff4ebb4637
          architecture: amd64
        - name: eu-central-1
          ami: ami-0c399bce2df1db665
          architecture: arm64
```

**Provider config type definitions** :

| Provider | Type definitions |
|---|---|
| AWS | [types_cloudprofile.go](https://github.com/gardener/gardener-extension-provider-aws/blob/master/pkg/apis/aws/types_cloudprofile.go) |
| GCP | [types_cloudprofile.go](https://github.com/gardener/gardener-extension-provider-gcp/blob/963932850e5c61abf9ffbad4b3dbeac097b2c86c/pkg/apis/gcp/types_cloudprofile.go) |
| Azure | [types_cloudprofile.go](https://github.com/gardener/gardener-extension-provider-azure/blob/master/pkg/apis/azure/types_cloudprofile.go) |
| Alicloud | [types_cloudprofile.go](https://github.com/gardener/gardener-extension-provider-alicloud/blob/master/pkg/apis/alicloud/types_cloudprofile.go) |
| OpenStack | [types_cloudprofile.go](https://github.com/gardener/gardener-extension-provider-openstack/blob/master/pkg/apis/openstack/types_cloudprofile.go) |
| NamespacedCloudProfile | [types_namespacedcloudprofile.go](https://github.com/gardener/gardener/blob/master/pkg/apis/core/types_namespacedcloudprofile.go) |

### Shoot Spec

The `Shoot` spec describes the Kubernetes cluster to create in Gardener — provider type, region, worker pools, networking, and the cloud profile to use.

**Type definition** :

- [types_shoot.go](https://github.com/gardener/gardener/blob/master/pkg/apis/core/types_shoot.go)

### Cluster Authentication in release-workflow

The release-workflow repo authenticates to the Gardener cluster using the `gardener/cc-utils` Kubernetes auth GitHub Action, which issues a short-lived kubeconfig via OIDC — no long-lived credentials are stored.

**References** :

- [kubernetes-auth action.yaml](https://github.com/gardener/cc-utils/blob/master/.github/actions/kubernetes-auth/action.yaml)
- [kubernetes_auth.py](https://github.com/gardener/cc-utils/blob/master/.github/actions/kubernetes-auth/kubernetes_auth.py)
- [SAP internal workflow example](https://github.tools.sap/kubernetes/test-infra/blob/master/.github/workflows/deploy-operator-secrets.yaml)

## Architecture

| File | Responsibility |
|---|---|
| `main.go` | CLI entry point; registers `publish` and `unpublish` sub-commands via Cobra |
| `publish.go` | S3/Vault I/O — manifest download, profile/shoot upload, credential plumbing |
| `unpublish.go` | S3 deletion of previously published specs |
| `nspcpfl.go` | Pure builder logic — parses raw manifests and constructs `NamespacedCloudProfile` objects for all five providers |
| `shootspec.go` | Pure builder logic — derives a `Shoot` spec (region, worker pool, infra config, networking) from a `NamespacedCloudProfile` |

## Key design points

- **Vault-mediated AWS credentials**: the tool never uses long-lived static credentials. It authenticates to Vault with a `VAULT_TOKEN` environment variable, then uses the returned short-lived AWS session tokens to access S3.
- **Namespace isolation**: all Gardener resources land in the `garden-gl-it` namespace, keeping integration test artefacts separate from production profiles.
- **Multi-architecture**: worker pools are emitted per architecture found in the manifests, so a single Shoot spec can test both `amd64` and `arm64` nodes when both are published.
- **YAML via JSON round-trip**: `runtime.RawExtension` fields (provider configs) cannot be marshalled directly to YAML by the standard library, so the tool marshals to JSON first and then converts — ensuring the embedded raw bytes are rendered correctly.
