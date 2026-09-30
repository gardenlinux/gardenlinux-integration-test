package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/goccy/go-yaml"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/spf13/cobra"
)

const (
	vaultServer    = "https://vault.tools.sap"
	vaultNamespace = "gardnlinux"
	manifestBucket = "gardenlinux-github-releases"
	manifestRegion = "eu-central-1"
	uploadBucket   = "gardenlinux-test-import"
)

func publishCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "publish",
		Short: "Publish a Garden Linux release",
		Args:  cobra.NoArgs,
		RunE:  runPublish,
	}

	c.Flags().StringP("version", "v", "", "release version (required)")
	c.Flags().StringP("commit", "c", "", "release commitish (required)")
	_ = c.MarkFlagRequired("version")
	_ = c.MarkFlagRequired("commit")

	return c
}

func unpublishCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "unpublish",
		Short: "Unpublish a Garden Linux release",
		Args:  cobra.NoArgs,
		RunE:  runUnpublish,
	}

	c.Flags().StringP("version", "v", "", "release version (required)")
	c.Flags().StringP("commit", "c", "", "release commitish (required)")
	_ = c.MarkFlagRequired("version")
	_ = c.MarkFlagRequired("commit")

	return c
}

// platformManifestKeys lists all platform/architecture combinations to download.
// Each entry maps to a manifest key: meta/singles/<prefix>-gardener_prod-<arch>-<ver>-<commit>
var platformManifestKeys = []struct {
	prefix     string // S3 key prefix (e.g. "aws")
	arch       string // architecture (e.g. "amd64")
	targetType string // TargetType used in BuildNSCloudProfiles
}{
	{"aws", "amd64", "AWS"},
	{"aws", "arm64", "AWS"},
	{"ali", "amd64", "Aliyun"},
	{"gcp", "amd64", "GCP"},
	{"gcp", "arm64", "GCP"},
	{"openstack", "amd64", "OpenStack"},
	{"azure", "amd64", "Azure"},
	{"azure", "arm64", "Azure"},
}

func runPublish(cmd *cobra.Command, _ []string) error {
	ver, _ := cmd.Flags().GetString("version")
	commit, _ := cmd.Flags().GetString("commit")

	var publications []Publication
	for _, p := range platformManifestKeys {
		m, err := downloadManifest(cmd.Context(), ver, commit, p.prefix, p.arch)
		if err != nil {
			fmt.Errorf("warning: skipping %s/%s: %v\n", p.prefix, p.arch, err)
			continue
		}
		publications = append(publications, Publication{TargetType: p.targetType, Manifest: m})
	}

	if len(publications) == 0 {
		return fmt.Errorf("no manifests downloaded successfully")
	}

	profiles, err := BuildNSCloudProfiles(ver, publications)
	if err != nil {
		return fmt.Errorf("cannot build NS cloud profiles: %w", err)
	}
	for _, profile := range profiles {
		profileYAML, err := ToYAML(profile)
		if err != nil {
			return fmt.Errorf("cannot marshal profile %s: %w", profile.Name, err)
		}

		// fmt.Printf("---\n%s", string(profileYAML))
		profileKey := fmt.Sprintf("meta/NSCloudProfile/%s/%s-%.8s", ver, profile.Name, commit)
		if err = uploadSpec(cmd.Context(), profileKey, profileYAML); err != nil {
			return fmt.Errorf("cannot upload profile %s: %w", profileKey, err)
		}

		var shootYAML []byte
		shootYAML, err = BuildShootSpecYAML(ver, profile)
		if err != nil {
			return fmt.Errorf("invalid shoot spec for %s: %w", profile.Name, err)
		}
		// fmt.Printf("---\n%s", string(shootYAML))
		shootKey := fmt.Sprintf("meta/ShootSpec/%s/%s-%.8s", ver, profile.Name, commit)
		err = uploadSpec(cmd.Context(), shootKey, shootYAML)
		if err != nil {
			return fmt.Errorf("cannot store ShootSpec %s: %w", profile.Name, err)
		}
	}

	return nil
}

func runUnpublish(cmd *cobra.Command, _ []string) error {
	ver, _ := cmd.Flags().GetString("version")
	commit, _ := cmd.Flags().GetString("commit")

	for _, p := range platformManifestKeys {
		_, err := downloadManifest(cmd.Context(), ver, commit, p.prefix, p.arch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s/%s: %v\n", p.prefix, p.arch, err)
		}
	}
	return nil
}

func uploadSpec(ctx context.Context, profileKey string, profileYAML []byte) error {
	s3Client, err := newS3Client(ctx, "se-aws-gardenlinux-integration-test/creds/glci")
	if err != nil {
		return fmt.Errorf("cannot create S3 client: %w", err)
	}
	fmt.Printf("Uploading profile: bucket=%s key=%s\n", uploadBucket, profileKey)

	err = putS3Object(ctx, s3Client, uploadBucket, profileKey, profileYAML)
	if err != nil {
		return fmt.Errorf("cannot upload profile %s: %w", profileKey, err)
	}
	return nil
}

func putS3Object(ctx context.Context, client *s3.Client, bucket string, key string, profileYAML []byte) error {
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:          &bucket,
		Key:             &key,
		Body:            bytes.NewReader(profileYAML),
		ContentEncoding: new("utf-8"),
		ContentType:     new("text/yaml"),
	})
	if err != nil {
		return err
	}
	return nil
}

// downloadManifest authenticates with Vault, downloads the manifest for the
// given version, commit, platform prefix, and architecture from S3,
// and returns the full parsed Manifest.
func downloadManifest(ctx context.Context, ver, commit, prefix, arch string) (*Manifest, error) {
	s3Client, err := newS3Client(ctx, "se-aws-gardenlinux/creds/glci")
	if err != nil {
		return nil, fmt.Errorf("cannot create S3 client: %w", err)
	}

	manifestKey := fmt.Sprintf("meta/singles/%s-gardener_prod-%s-%s-%.8s", prefix, arch, ver, commit)
	fmt.Printf("Downloading manifest: bucket=%s key=%s\n", manifestBucket, manifestKey)

	body, err := getS3Object(ctx, s3Client, manifestBucket, manifestKey)
	if err != nil {
		return nil, fmt.Errorf("cannot download manifest: %w", err)
	}
	defer body.Close()

	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("cannot read manifest: %w", err)
	}

	var m Manifest
	if err = yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("cannot parse manifest: %w", err)
	}

	if m.PublishedImageMetadata == nil {
		return nil, fmt.Errorf("manifest has no published_image_metadata")
	}

	return &m, nil
}

// newS3Client creates an S3 client using credentials obtained from Vault.
func newS3Client(ctx context.Context, vaultAWSPath string) (*s3.Client, error) {
	token := os.Getenv("VAULT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("VAULT_TOKEN environment variable is not set")
	}

	awsKey, awsSecret, awsToken, err := vaultAWSCreds(ctx, token, vaultAWSPath)
	if err != nil {
		return nil, fmt.Errorf("cannot obtain AWS credentials from Vault: %w", err)
	}

	cfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithRegion(manifestRegion),
		awscfg.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(awsKey, awsSecret, awsToken),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("cannot load AWS config: %w", err)
	}

	return s3.NewFromConfig(cfg), nil
}

// vaultAWSCreds logs into Vault with the given token and reads AWS credentials
// for the given Vault AWS secrets path.
func vaultAWSCreds(ctx context.Context, token, vaultAWSPath string) (key, secret, sessionToken string, err error) {
	cfg := vaultapi.DefaultConfig()
	cfg.Address = vaultServer

	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		return "", "", "", fmt.Errorf("cannot create Vault client: %w", err)
	}

	client.SetToken(token)
	client.SetNamespace(vaultNamespace)

	_, err2 := client.Auth().Token().LookupSelfWithContext(ctx)
	if err2 != nil {
		return "", "", "", fmt.Errorf("Vault token validation failed: %w", err2)
	}

	data, err2 := client.Logical().ReadWithContext(ctx, vaultAWSPath)
	if err2 != nil {
		return "", "", "", fmt.Errorf("cannot read AWS credentials from Vault path %s: %w", vaultAWSPath, err2)
	}
	if data == nil || data.Data == nil {
		return "", "", "", fmt.Errorf("empty response from Vault path %s", vaultAWSPath)
	}

	key, _ = data.Data["access_key"].(string)
	secret, _ = data.Data["secret_key"].(string)
	sessionToken, _ = data.Data["security_token"].(string)

	if key == "" || secret == "" {
		return "", "", "", fmt.Errorf("incomplete AWS credentials from Vault: missing access_key or secret_key")
	}

	return key, secret, sessionToken, nil
}

func getS3Object(ctx context.Context, client *s3.Client, bucket, key string) (io.ReadCloser, error) {
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}

	return out.Body, nil
}
