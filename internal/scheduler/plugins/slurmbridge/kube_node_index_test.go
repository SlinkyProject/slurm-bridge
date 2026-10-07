// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	fwk "k8s.io/kube-scheduler/framework"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type nodeIndexTestHandle struct {
	fwk.Handle
	factory informers.SharedInformerFactory
}

func (h *nodeIndexTestHandle) SharedInformerFactory() informers.SharedInformerFactory {
	return h.factory
}

func (h *nodeIndexTestHandle) KubeConfig() *rest.Config {
	return &rest.Config{Host: "https://kubernetes.test", Transport: kubeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected Kubernetes request: %s", req.URL)
	})}
}

func TestNewRegistersKubeNodeIndex(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.SlurmBridgeGenericWorkload, false)
	originalConfigFile := ConfigFile
	t.Cleanup(func() { ConfigFile = originalConfigFile })
	ConfigFile = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(ConfigFile, []byte("schedulerName: slurm-bridge\nslurmRestApi: http://slurm.test\nmcsLabel: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	factory := informers.NewSharedInformerFactory(fake.NewClientset(), 0)
	plugin, err := New(t.Context(), nil, &nodeIndexTestHandle{factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	if plugin.(*SlurmBridge).kubeNodeIndex == nil {
		t.Fatal("indexed lookup is not configured")
	}
	if _, registered := factory.Core().V1().Nodes().Informer().GetIndexer().GetIndexers()[nodeutils.IndexFieldSlurmNodeName]; !registered {
		t.Fatal("Node index is not registered")
	}
}

// testKubeNodeIndex seeds a synchronized index from existing scheduler fixtures.
// The informer lifecycle itself is exercised in TestIndexedSlurmToKubeNodes.
func testKubeNodeIndex(t *testing.T, reader kubeclient.Reader) *kubeNodeNameIndex {
	t.Helper()
	nodes := &corev1.NodeList{}
	if err := reader.List(t.Context(), nodes); err != nil {
		t.Fatal(err)
	}
	factory := informers.NewSharedInformerFactory(fake.NewClientset(), 0)
	index, err := newKubeNodeNameIndex(factory.Core().V1().Nodes().Informer())
	if err != nil {
		t.Fatal(err)
	}
	for i := range nodes.Items {
		if err := index.index.Add(&nodes.Items[i]); err != nil {
			t.Fatal(err)
		}
	}
	index.hasSynced = func() bool { return true }
	return index
}

func TestIndexedSlurmToKubeNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker-a", Labels: map[string]string{wellknown.LabelSlurmNodeName: "cn1"},
	}}
	cs := fake.NewClientset(node, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-b"}})
	factory := informers.NewSharedInformerFactory(cs, 0)
	informer := factory.Core().V1().Nodes().Informer()
	index, err := newKubeNodeNameIndex(informer)
	if err != nil {
		t.Fatal(err)
	}
	// Profiles share an informer, so registering the same index again is valid.
	if _, err := newKubeNodeNameIndex(informer); err != nil {
		t.Fatal(err)
	}
	// No Kubernetes client is supplied: any accidental live fallback fails the test.
	sb := &SlurmBridge{kubeNodeIndex: index}
	if _, err := sb.slurmToKubeNodes(ctx, []string{"cn1"}); err == nil {
		t.Fatal("unsynced lookup succeeded")
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		t.Fatal("node informer did not sync")
	}
	cs.ClearActions()
	got, err := sb.slurmToKubeNodes(ctx, []string{"cn1", "worker-b", "cn1"})
	if err != nil || !got.Equal(sets.New("worker-a", "worker-b")) {
		t.Fatalf("lookup = %v, %v", got, err)
	}
	// Exact Kubernetes names remain valid even when the Node has a Slurm alias.
	got, err = sb.slurmToKubeNodes(ctx, []string{"worker-a"})
	if err != nil || !got.Equal(sets.New("worker-a")) {
		t.Fatalf("name fallback = %v, %v", got, err)
	}
	if _, err := sb.slurmToKubeNodes(ctx, []string{"missing"}); !errors.Is(err, ErrorNoKubeNodeMatch) {
		t.Fatalf("missing node error = %v", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := sb.slurmToKubeNodes(canceled, []string{"cn1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup error = %v", err)
	}

	eventually := func(check func() bool) {
		t.Helper()
		if err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 5*time.Second, true,
			func(context.Context) (bool, error) { return check(), nil }); err != nil {
			t.Fatal(err)
		}
	}
	updated := node.DeepCopy()
	updated.Labels[wellknown.LabelSlurmNodeName] = "cn2"
	if _, err := cs.CoreV1().Nodes().Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(func() bool {
		_, oldErr := sb.slurmToKubeNodes(ctx, []string{"cn1"})
		got, err := sb.slurmToKubeNodes(ctx, []string{"cn2"})
		return errors.Is(oldErr, ErrorNoKubeNodeMatch) && err == nil && got.Has("worker-a")
	})

	duplicate := updated.DeepCopy()
	duplicate.Name = "worker-c"
	if _, err := cs.CoreV1().Nodes().Create(ctx, duplicate, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(func() bool {
		_, err := sb.slurmToKubeNodes(ctx, []string{"cn2"})
		return err != nil && strings.Contains(err.Error(), "multiple Kubernetes nodes")
	})
	if err := cs.CoreV1().Nodes().Delete(ctx, "worker-a", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(func() bool {
		got, err := sb.slurmToKubeNodes(ctx, []string{"cn2"})
		_, deletedErr := sb.slurmToKubeNodes(ctx, []string{"worker-a"})
		return err == nil && got.Equal(sets.New("worker-c")) && errors.Is(deletedErr, ErrorNoKubeNodeMatch)
	})
	// Removing an alias restores the Node's own name as its indexed Slurm name.
	unlabeled := duplicate.DeepCopy()
	delete(unlabeled.Labels, wellknown.LabelSlurmNodeName)
	if _, err := cs.CoreV1().Nodes().Update(ctx, unlabeled, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(func() bool {
		_, aliasErr := sb.slurmToKubeNodes(ctx, []string{"cn2"})
		got, err := sb.slurmToKubeNodes(ctx, []string{"worker-c"})
		return errors.Is(aliasErr, ErrorNoKubeNodeMatch) && err == nil && got.Equal(sets.New("worker-c"))
	})
	for _, action := range cs.Actions() {
		if action.GetVerb() == "get" || action.GetVerb() == "list" {
			t.Fatalf("unexpected live node read after informer sync: %s", action.GetVerb())
		}
	}
}
