// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	ptr "k8s.io/utils/ptr"
)

// getBasePath returns the fully qualified path of the slurm-operator repo within the context in which `go test` is called
func GetBasePath() string {
	_, b, _, _ := runtime.Caller(0)
	fullpath := filepath.Dir(b)
	path, _ := strings.CutSuffix(fullpath, "test")

	return path
}

// BuildBridgeImages builds images for Slurm-bridge
func BuildBridgeImages(scheduler string, controllers string, admission string) error {
	imageOS := runtime.GOOS
	imageArch := runtime.GOARCH

	imagePlatform := imageOS + "/" + imageArch
	buildArgs := map[string]*string{
		"TARGETOS":      ptr.To(imageOS),
		"TARGETARCH":    ptr.To(imageArch),
		"BUILDPLATFORM": ptr.To(imagePlatform),
	}

	// Build slurm-bridge image
	var bridgeTags []string
	bridgeTags = append(bridgeTags, scheduler)
	err := DockerBuild(bridgeTags, "scheduler", "Dockerfile", Basepath, buildArgs)
	if err != nil {
		return err
	}

	// Build controllers image
	var controllersTags []string
	controllersTags = append(controllersTags, controllers)
	err = DockerBuild(controllersTags, "controllers", "Dockerfile", Basepath, buildArgs)
	if err != nil {
		return err
	}

	// Build image
	var admissionTags []string
	admissionTags = append(admissionTags, admission)
	err = DockerBuild(admissionTags, "admission", "Dockerfile", Basepath, buildArgs)
	if err != nil {
		return err
	}

	return nil
}

// DockerBuild builds a Docker image from the provided parameters
func DockerBuild(imageTags []string, imageTarget string, dockerfile string, dockerfilePath string, buildArgs map[string]*string) error {
	args := []string{"build",
		"--file", filepath.Join(dockerfilePath, dockerfile),
		"--target", imageTarget,
	}
	for _, tag := range imageTags {
		args = append(args, "--tag", tag)
	}
	for key, val := range buildArgs {
		if val != nil {
			args = append(args, "--build-arg", fmt.Sprintf("%s=%s", key, *val))
		}
	}
	args = append(args, dockerfilePath)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func RetryCommand(ctx context.Context, t *testing.T, command string, args []string, wants string, cleanup_command string, cleanup_args []string, retries int, retryDelay time.Duration) context.Context {
	for retry := range retries {

		if cleanup_command != "" && len(cleanup_args) > 0 {
			cleanup_cmd := exec.Command(cleanup_command, cleanup_args...)

			_, _ = cleanup_cmd.Output() //nolint:errcheck
		}

		cmd := exec.Command(command, args...)

		output, err := cmd.Output()
		if err == nil && (wants == "" || strings.TrimSpace(string(output)) == wants) {
			return ctx
		}

		if retry == retries-retry {
			if err != nil {
				t.Fatalf("failed running '%v %v': %v", command, args, err)
			}
			if string(output) != "" {
				t.Fatalf("assertion failed. wants: %v, got: %v", wants, string(output))
			}

			return ctx
		}

		time.Sleep(retryDelay)
	}

	return ctx
}

func GetSlurmNodeInfo(nodeName string) (map[string]string, error) {
	command := "kubectl"
	args := []string{
		"exec", "-n", SlurmNamespace, "slurm-controller-0", "--",
		"scontrol", "show", "node", nodeName,
	}

	cmd := exec.Command(command, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("failed executing command")
	}

	out_map := StringToMap(string(output))
	return out_map, nil
}

func StringToMap(input string) map[string]string {
	out_array := strings.Split(string(input), " ")
	out_map := make(map[string]string)

	for _, val := range out_array {
		object := strings.Split(val, "=")
		if len(object) == 2 {
			key := object[0]
			value := object[1]

			out_map[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	return out_map
}
