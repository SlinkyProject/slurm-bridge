// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func TestLWSTPUAlias(t *testing.T) {
	requireE2ESuite(t, "tpu", true)
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseE2ECleanupFromEnvironment(); err != nil {
		t.Fatal(err)
	}
	testEnv.Test(t, withSuiteLabel(testLWSTPUAlias(), "tpu"))
}

func testLWSTPUAlias() types.Feature {
	lws := nativeLWS(envconf.RandomName("lws-tpu", 24), 1, 2)
	lws.Spec.PodTerminationPolicy = lwsv1.ParallelPodTerminationPolicy
	worker := &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
	worker.Resources.Requests["google.com/tpu"] = resource.MustParse("4")
	worker.Resources.Limits["google.com/tpu"] = resource.MustParse("4")
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Tolerations = append(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Tolerations,
		corev1.Toleration{Key: "google.com/tpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule})
	var aliasClass *resourcev1.DeviceClass
	return features.New("LWS TPU alias allocation").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			requireKubernetesPodGroupAPI(t, config, podGroupV1Beta1)
			c := nativeLWSClient(t, config)
			aliasClass = getTPUAliasClass(ctx, t, c)
			if err := c.Create(ctx, lws); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Assess("LWS detects TPU workers and bridge allocates the alias", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			pods := waitForNativeLWSPods(ctx, t, config, lws, 1, 2)
			c := nativeLWSClient(t, config)
			for i := range pods {
				pod := &pods[i]
				if pod.Labels[lwsv1.WorkerIndexLabelKey] == "0" {
					if pod.Status.ExtendedResourceClaimStatus != nil {
						t.Fatal("CPU leader received a TPU claim")
					}
					continue
				}
				assertTPUAliasClaim(ctx, t, c, pod, aliasClass.Name)
				env := map[string]string{}
				for _, variable := range pod.Spec.Containers[0].Env {
					env[variable.Name] = variable.Value
				}
				hostname := pod.Name + "." + pod.Spec.Subdomain
				if env["TPU_WORKER_ID"] != "0" || env["TPU_WORKER_HOSTNAMES"] != hostname || env["TPU_PROCESS_ADDRESSES"] != hostname+":8476" {
					t.Fatalf("LWS did not inject single-host TPU identity and peers: %v", env)
				}
				output, err := execInPod(ctx, config, pod, "sh", "-ec", `for i in 0 1 2 3; do test -b /dev/accel$i; done
test "$TPU_WORKER_ID" = 0
test "$TPU_PROCESS_ADDRESSES" = "$1"`, "--", env["TPU_PROCESS_ADDRESSES"])
				if err != nil {
					t.Fatalf("LWS TPU runtime injection: %v; %s", err, output)
				}
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			cleanupNativeLWS(ctx, t, config, lws)
			return ctx
		}).Feature()
}
