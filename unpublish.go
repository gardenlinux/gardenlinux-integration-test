package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

func runUnpublish(cmd *cobra.Command, _ []string) error {
	ver, _ := cmd.Flags().GetString("version")
	commit, _ := cmd.Flags().GetString("commit")

	var publications []Publication
	for _, p := range platformManifestKeys {
		m, err := downloadManifest(cmd.Context(), ver, commit, p.prefix, p.arch)
		if err != nil {
			fmt.Printf("warning: skipping %s/%s: %v\n", p.prefix, p.arch, err)
			continue
		}
		publications = append(publications, Publication{TargetType: p.targetType, Manifest: m})
	}

	nsCloudProfileKeys, shootSpecKeys := collectNSCloudProfileKeys(ver, commit, publications)

	for _, key := range nsCloudProfileKeys {
		if err := deleteSpec(cmd.Context(), key); err != nil {
			fmt.Printf("warning: cannot remove NSCloudProfile %s: %v\n", key, err)
		} else {
			fmt.Printf("Removed NSCloudProfile: %s\n", key)
		}
	}
	for _, key := range shootSpecKeys {
		if err := deleteSpec(cmd.Context(), key); err != nil {
			fmt.Printf("warning: cannot remove ShootSpec %s: %v\n", key, err)
		} else {
			fmt.Printf("Removed ShootSpec: %s\n", key)
		}
	}
	return nil
}

func collectNSCloudProfileKeys(version, commit string, publications []Publication) (nsProfileKeys, shootKeys []string) {
	profiles, err := BuildNSCloudProfiles(version, publications)
	if err != nil {
		fmt.Printf("warning: cannot rebuild NSCloudProfiles for cleanup: %v\n", err)
		return nil, nil
	}
	for _, profile := range profiles {
		nsProfileKeys = append(nsProfileKeys, fmt.Sprintf("meta/NSCloudProfile/%s/%s-%.8s", version, profile.Name, commit))
		shootKeys = append(shootKeys, fmt.Sprintf("meta/ShootSpec/%s/%s-%.8s", version, profile.Name, commit))
	}
	return nsProfileKeys, shootKeys
}

func deleteSpec(ctx context.Context, key string) error {
	s3Client, err := newS3Client(ctx, "se-aws-gardenlinux-integration-test/creds/glci")
	if err != nil {
		return fmt.Errorf("cannot create S3 client: %w", err)
	}
	return deleteS3Object(ctx, s3Client, uploadBucket, key)
}

func deleteS3Object(ctx context.Context, client *s3.Client, bucket, key string) error {
	_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	return err
}
