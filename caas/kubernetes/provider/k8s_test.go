// Copyright 2018 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package provider_test

import (
	"time"

	"github.com/golang/mock/gomock"
	"github.com/juju/clock/testclock"
	"github.com/juju/errors"
	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils/set"
	"github.com/juju/version"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/worker.v1/workertest"
	apps "k8s.io/api/apps/v1"
	appsv1 "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/juju/juju/caas"
	"github.com/juju/juju/caas/kubernetes/provider"
	"github.com/juju/juju/core/application"
	"github.com/juju/juju/core/constraints"
	"github.com/juju/juju/core/devices"
	"github.com/juju/juju/core/status"
	"github.com/juju/juju/environs/context"
	"github.com/juju/juju/storage"
	"github.com/juju/juju/testing"
)

type K8sSuite struct {
	testing.BaseSuite
}

var _ = gc.Suite(&K8sSuite{})

func (s *K8sSuite) TestMakeUnitSpecNoConfigConfig(c *gc.C) {
	podSpec := caas.PodSpec{
		ProviderPod: &provider.K8sPodSpec{
			ActiveDeadlineSeconds:         int64Ptr(10),
			ServiceAccountName:            "serviceAccount",
			Hostname:                      "host",
			Subdomain:                     "sub",
			DNSPolicy:                     core.DNSClusterFirst,
			TerminationGracePeriodSeconds: int64Ptr(20),
			RestartPolicy:                 core.RestartPolicyOnFailure,
			AutomountServiceAccountToken:  boolPtr(true),
			Priority:                      int32Ptr(30),
			PriorityClassName:             "top",
			DNSConfig: &core.PodDNSConfig{
				Nameservers: []string{"ns1", "n2"},
			},
			SecurityContext: &core.PodSecurityContext{
				RunAsNonRoot: boolPtr(true),
			},
			ReadinessGates: []core.PodReadinessGate{
				{ConditionType: core.PodInitialized},
			},
		},
		Containers: []caas.ContainerSpec{{
			Name:  "test",
			Ports: []caas.ContainerPort{{ContainerPort: 80, Protocol: "TCP"}},
			Image: "juju/image",
			ProviderContainer: &provider.K8sContainerSpec{
				ImagePullPolicy: core.PullAlways,
				ReadinessProbe: &core.Probe{
					InitialDelaySeconds: 10,
					Handler:             core.Handler{HTTPGet: &core.HTTPGetAction{Path: "/ready"}},
				},
				LivenessProbe: &core.Probe{
					SuccessThreshold: 20,
					Handler:          core.Handler{HTTPGet: &core.HTTPGetAction{Path: "/liveready"}},
				},
			},
		}, {
			Name:  "test2",
			Ports: []caas.ContainerPort{{ContainerPort: 8080, Protocol: "TCP"}},
			Image: "juju/image2",
		}},
	}
	spec, err := provider.MakeUnitSpec("app-name", "app-name", &podSpec)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(provider.PodSpec(spec), jc.DeepEquals, core.PodSpec{
		ActiveDeadlineSeconds:         int64Ptr(10),
		ServiceAccountName:            "serviceAccount",
		Hostname:                      "host",
		Subdomain:                     "sub",
		DNSPolicy:                     core.DNSClusterFirst,
		TerminationGracePeriodSeconds: int64Ptr(20),
		RestartPolicy:                 core.RestartPolicyOnFailure,
		AutomountServiceAccountToken:  boolPtr(true),
		Priority:                      int32Ptr(30),
		PriorityClassName:             "top",
		DNSConfig: &core.PodDNSConfig{
			Nameservers: []string{"ns1", "n2"},
		},
		SecurityContext: &core.PodSecurityContext{
			RunAsNonRoot: boolPtr(true),
		},
		ReadinessGates: []core.PodReadinessGate{
			{ConditionType: core.PodInitialized},
		},
		Containers: []core.Container{
			{
				Name:            "test",
				Image:           "juju/image",
				Ports:           []core.ContainerPort{{ContainerPort: int32(80), Protocol: core.ProtocolTCP}},
				ImagePullPolicy: core.PullAlways,
				ReadinessProbe: &core.Probe{
					InitialDelaySeconds: 10,
					Handler:             core.Handler{HTTPGet: &core.HTTPGetAction{Path: "/ready"}},
				},
				LivenessProbe: &core.Probe{
					SuccessThreshold: 20,
					Handler:          core.Handler{HTTPGet: &core.HTTPGetAction{Path: "/liveready"}},
				},
			}, {
				Name:  "test2",
				Image: "juju/image2",
				Ports: []core.ContainerPort{{ContainerPort: int32(8080), Protocol: core.ProtocolTCP}},
			},
		},
	})
}

var basicPodspec = &caas.PodSpec{
	Containers: []caas.ContainerSpec{{
		Name:         "test",
		Ports:        []caas.ContainerPort{{ContainerPort: 80, Protocol: "TCP"}},
		ImageDetails: caas.ImageDetails{ImagePath: "juju/image", Username: "fred", Password: "secret"},
		Command:      []string{"sh", "-c"},
		Args:         []string{"doIt", "--debug"},
		WorkingDir:   "/path/to/here",
		Config: map[string]interface{}{
			"foo":        "bar",
			"restricted": "'yes'",
			"bar":        true,
			"switch":     "on",
		},
	}, {
		Name:  "test2",
		Ports: []caas.ContainerPort{{ContainerPort: 8080, Protocol: "TCP", Name: "fred"}},
		Image: "juju/image2",
	}},
}

var operatorPodspec = core.PodSpec{
	Containers: []core.Container{{
		Name:            "juju-operator",
		ImagePullPolicy: core.PullIfNotPresent,
		Image:           "/path/to/image",
		Env: []core.EnvVar{
			{Name: "JUJU_APPLICATION", Value: "test"},
		},
		VolumeMounts: []core.VolumeMount{{
			Name:      "test-operator-config",
			MountPath: "path/to/agent/agents/application-test/template-agent.conf",
			SubPath:   "template-agent.conf",
		}, {
			Name:      "charm",
			MountPath: "path/to/agent/agents",
		}},
	}},
	Volumes: []core.Volume{{
		Name: "test-operator-config",
		VolumeSource: core.VolumeSource{
			ConfigMap: &core.ConfigMapVolumeSource{
				LocalObjectReference: core.LocalObjectReference{
					Name: "test-operator-config",
				},
				Items: []core.KeyToPath{{
					Key:  "test-agent.conf",
					Path: "template-agent.conf",
				}},
			},
		},
	}},
}

var basicServiceArg = &core.Service{
	ObjectMeta: v1.ObjectMeta{
		Name:   "app-name",
		Labels: map[string]string{"juju-application": "app-name"}},
	Spec: core.ServiceSpec{
		Selector: map[string]string{"juju-application": "app-name"},
		Type:     "nodeIP",
		Ports: []core.ServicePort{
			{Port: 80, TargetPort: intstr.FromInt(80), Protocol: "TCP"},
			{Port: 8080, Protocol: "TCP", Name: "fred"},
		},
		LoadBalancerIP: "10.0.0.1",
		ExternalName:   "ext-name",
	},
}

func (s *K8sBrokerSuite) secretArg(c *gc.C, labels map[string]string) *core.Secret {
	secretData, err := provider.CreateDockerConfigJSON(&basicPodspec.Containers[0].ImageDetails)
	c.Assert(err, jc.ErrorIsNil)
	secret := &core.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      "app-name-test-secret",
			Namespace: "test",
			Labels:    labels,
		},
		Type: "kubernetes.io/dockerconfigjson",
		Data: map[string][]byte{".dockerconfigjson": secretData},
	}
	if secret.Labels == nil {
		secret.Labels = make(map[string]string)
	}
	secret.Labels["juju-application"] = "app-name"
	return secret
}

func (s *K8sSuite) TestMakeUnitSpecConfigPairs(c *gc.C) {
	spec, err := provider.MakeUnitSpec("app-name", "app-name", basicPodspec)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(provider.PodSpec(spec), jc.DeepEquals, core.PodSpec{
		ImagePullSecrets: []core.LocalObjectReference{{Name: "app-name-test-secret"}},
		Containers: []core.Container{
			{
				Name:       "test",
				Image:      "juju/image",
				Ports:      []core.ContainerPort{{ContainerPort: int32(80), Protocol: core.ProtocolTCP}},
				Command:    []string{"sh", "-c"},
				Args:       []string{"doIt", "--debug"},
				WorkingDir: "/path/to/here",
				Env: []core.EnvVar{
					{Name: "bar", Value: "true"},
					{Name: "foo", Value: "bar"},
					{Name: "restricted", Value: "yes"},
					{Name: "switch", Value: "true"},
				},
			}, {
				Name:  "test2",
				Image: "juju/image2",
				Ports: []core.ContainerPort{{ContainerPort: int32(8080), Protocol: core.ProtocolTCP, Name: "fred"}},
			},
		},
	})
}

func (s *K8sSuite) TestOperatorPodConfig(c *gc.C) {
	tags := map[string]string{
		"juju-operator": "gitlab",
	}
	pod := provider.OperatorPod("gitlab", "gitlab", "/var/lib/juju", "jujusolutions/caas-jujud-operator", "2.99.0", tags)
	c.Assert(pod.Name, gc.Equals, "gitlab")
	c.Assert(pod.Labels, jc.DeepEquals, map[string]string{
		"juju-operator": "gitlab",
		"juju-version":  "2.99.0",
	})
	c.Assert(pod.Spec.Containers, gc.HasLen, 1)
	c.Assert(pod.Spec.Containers[0].Image, gc.Equals, "jujusolutions/caas-jujud-operator")
	c.Assert(pod.Spec.Containers[0].VolumeMounts, gc.HasLen, 1)
	c.Assert(pod.Spec.Containers[0].VolumeMounts[0].MountPath, gc.Equals, "/var/lib/juju/agents/application-gitlab/template-agent.conf")
}

type K8sBrokerSuite struct {
	BaseSuite
}

var _ = gc.Suite(&K8sBrokerSuite{})

func (s *K8sBrokerSuite) TestConfig(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	c.Assert(s.broker.Config(), jc.DeepEquals, s.cfg)
}

type hostRegionTestcase struct {
	expectedOut set.Strings
	nodes       *core.NodeList
}

var hostRegionsTestCases = []hostRegionTestcase{
	{
		expectedOut: set.NewStrings(),
		nodes:       newNodeList(map[string]string{}),
	},
	{
		expectedOut: set.NewStrings(),
		nodes: newNodeList(map[string]string{
			"cloud.google.com/gke-nodepool": "",
		}),
	},
	{
		expectedOut: set.NewStrings(),
		nodes: newNodeList(map[string]string{
			"cloud.google.com/gke-os-distribution": "",
		}),
	},
	{
		expectedOut: set.NewStrings(),
		nodes: newNodeList(map[string]string{
			"cloud.google.com/gke-nodepool":        "",
			"cloud.google.com/gke-os-distribution": "",
		}),
	},
	{
		expectedOut: set.NewStrings(),
		nodes: newNodeList(map[string]string{
			"kubernetes.azure.com/cluster": "",
		}),
	},
	{
		expectedOut: set.NewStrings(),
		nodes: newNodeList(map[string]string{
			"manufacturer": "amazon_ec2",
		}),
	},
	{
		expectedOut: set.NewStrings(),
		nodes: newNodeList(map[string]string{
			"failure-domain.beta.kubernetes.io/region": "a-fancy-region",
		}),
	},
	{
		expectedOut: set.NewStrings(),
		nodes: newNodeList(map[string]string{
			"failure-domain.beta.kubernetes.io/region": "a-fancy-region",
			"cloud.google.com/gke-nodepool":            "",
		}),
	},
	{
		expectedOut: set.NewStrings(),
		nodes: newNodeList(map[string]string{
			"failure-domain.beta.kubernetes.io/region": "a-fancy-region",
			"cloud.google.com/gke-os-distribution":     "",
		}),
	},
	{
		expectedOut: set.NewStrings("gce/a-fancy-region"),
		nodes: newNodeList(map[string]string{
			"failure-domain.beta.kubernetes.io/region": "a-fancy-region",
			"cloud.google.com/gke-nodepool":            "",
			"cloud.google.com/gke-os-distribution":     "",
		}),
	},
	{
		expectedOut: set.NewStrings("azure/a-fancy-region"),
		nodes: newNodeList(map[string]string{
			"failure-domain.beta.kubernetes.io/region": "a-fancy-region",
			"kubernetes.azure.com/cluster":             "",
		}),
	},
	{
		expectedOut: set.NewStrings("ec2/a-fancy-region"),
		nodes: newNodeList(map[string]string{
			"failure-domain.beta.kubernetes.io/region": "a-fancy-region",
			"manufacturer": "amazon_ec2",
		}),
	},
}

func newNodeList(labels map[string]string) *core.NodeList {
	return &core.NodeList{Items: []core.Node{newNode(labels)}}
}

func (s *K8sBrokerSuite) TestListHostCloudRegions(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	for _, v := range hostRegionsTestCases {
		gomock.InOrder(
			s.mockNodes.EXPECT().List(v1.ListOptions{Limit: 5}).Times(1).
				Return(v.nodes, nil),
		)
		regions, err := s.broker.ListHostCloudRegions()
		c.Check(err, jc.ErrorIsNil)
		c.Check(regions, jc.DeepEquals, v.expectedOut)
	}
}

func (s *K8sBrokerSuite) TestSetConfig(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	err := s.broker.SetConfig(s.cfg)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureNamespace(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	ns := &core.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test"}}
	gomock.InOrder(
		s.mockNamespaces.EXPECT().Update(ns).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockNamespaces.EXPECT().Create(ns).Times(1),
		// Idempotent check.
		s.mockNamespaces.EXPECT().Update(ns).Times(1),
	)

	err := s.broker.EnsureNamespace()
	c.Assert(err, jc.ErrorIsNil)

	// Check idempotent.
	err = s.broker.EnsureNamespace()
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestGetNamespace(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	ns := &core.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test"}}
	gomock.InOrder(
		s.mockNamespaces.EXPECT().Get("test", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(ns, nil),
	)

	out, err := s.broker.GetNamespace("test")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(out, jc.DeepEquals, ns)
}

func (s *K8sBrokerSuite) TestGetNamespaceNotFound(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	gomock.InOrder(
		s.mockNamespaces.EXPECT().Get("unknown-namespace", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
	)

	out, err := s.broker.GetNamespace("unknown-namespace")
	c.Assert(err, jc.Satisfies, errors.IsNotFound)
	c.Assert(out, gc.IsNil)
}

func (s *K8sBrokerSuite) TestNamespaces(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	ns1 := core.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test"}}
	ns2 := core.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test2"}}
	gomock.InOrder(
		s.mockNamespaces.EXPECT().List(v1.ListOptions{IncludeUninitialized: true}).Times(1).
			Return(&core.NamespaceList{Items: []core.Namespace{ns1, ns2}}, nil),
	)

	result, err := s.broker.Namespaces()
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(result, jc.SameContents, []string{"test", "test2"})
}

func (s *K8sBrokerSuite) TestDestroy(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	ns := &core.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test"}}
	namespaceWatcher := s.k8sNewFakeWatcher()

	gomock.InOrder(
		s.mockNamespaces.EXPECT().Watch(
			v1.ListOptions{
				FieldSelector:        fields.OneTermEqualSelector("metadata.name", "test").String(),
				IncludeUninitialized: true,
			},
		).
			Return(namespaceWatcher, nil),
		s.mockNamespaces.EXPECT().Delete("test", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(nil),
		s.mockStorageClass.EXPECT().DeleteCollection(
			s.deleteOptions(v1.DeletePropagationForeground),
			v1.ListOptions{LabelSelector: "juju-model==test"},
		).Times(1).
			Return(s.k8sNotFoundError()),
		// still terminating.
		s.mockNamespaces.EXPECT().Get("test", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(ns, nil),
		// terminated, not found returned.
		s.mockNamespaces.EXPECT().Get("test", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
	)

	go func(w *watch.RaceFreeFakeWatcher, clk *testclock.Clock) {
		for _, f := range []func(runtime.Object){w.Add, w.Modify, w.Delete} {
			if !w.IsStopped() {
				clk.WaitAdvance(time.Second, testing.LongWait, 1)
				f(ns)
			}
		}
	}(namespaceWatcher, s.clock)

	err := s.broker.Destroy(context.NewCloudCallContext())
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(workertest.CheckKilled(c, s.watcher), jc.ErrorIsNil)
	c.Assert(namespaceWatcher.IsStopped(), jc.IsTrue)
}

func (s *K8sBrokerSuite) TestDeleteOperator(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	// Delete operations below return a not found to ensure it's treated as a no-op.
	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-test", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockConfigMaps.EXPECT().Delete("test-operator-config", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockConfigMaps.EXPECT().Delete("test-configurations-config", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Delete("test-operator", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockPods.EXPECT().List(v1.ListOptions{LabelSelector: "juju-operator==test"}).
			Return(&core.PodList{Items: []core.Pod{{
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:         "jujud",
						VolumeMounts: []core.VolumeMount{{Name: "test-operator-volume"}},
					}},
					Volumes: []core.Volume{{
						Name: "test-operator-volume", VolumeSource: core.VolumeSource{
							PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{
								ClaimName: "test-operator-volume"}},
					}},
				},
			}}}, nil),
		s.mockSecrets.EXPECT().Delete("test-jujud-secret", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockPersistentVolumeClaims.EXPECT().Delete("test-operator-volume", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockPersistentVolumes.EXPECT().Delete("test-operator-volume", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockDeployments.EXPECT().Delete("test-operator", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
	)

	err := s.broker.DeleteOperator("test")
	c.Assert(err, jc.ErrorIsNil)
}

func operatorStatefulSetArg(numUnits int32, scName string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: v1.ObjectMeta{
			Name: "test-operator",
			Labels: map[string]string{
				"juju-operator": "test",
				"juju-version":  "2.99.0",
				"fred":          "mary",
			}},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &numUnits,
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{"juju-operator": "test"},
			},
			Template: core.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{
						"juju-operator": "test",
						"fred":          "mary",
						"juju-version":  "2.99.0",
					},
				},
				Spec: operatorPodspec,
			},
			VolumeClaimTemplates: []core.PersistentVolumeClaim{{
				ObjectMeta: v1.ObjectMeta{
					Name: "charm",
					Labels: map[string]string{
						"juju-operator": "test",
						"foo":           "bar",
					}},
				Spec: core.PersistentVolumeClaimSpec{
					StorageClassName: &scName,
					AccessModes:      []core.PersistentVolumeAccessMode{core.ReadWriteOnce},
					Resources: core.ResourceRequirements{
						Requests: core.ResourceList{
							core.ResourceStorage: resource.MustParse("10Mi"),
						},
					},
				},
			}},
			PodManagementPolicy: apps.ParallelPodManagement,
		},
	}
}

func unitStatefulSetArg(numUnits int32, scName string, podSpec core.PodSpec) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: v1.ObjectMeta{
			Name:   "app-name",
			Labels: map[string]string{"juju-application": "app-name"}},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &numUnits,
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{"juju-application": "app-name"},
			},
			Template: core.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{"juju-application": "app-name"},
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: []core.PersistentVolumeClaim{{
				ObjectMeta: v1.ObjectMeta{
					Name: "database-0",
					Labels: map[string]string{
						"juju-application": "app-name",
						"foo":              "bar",
						"juju-storage":     "database",
					}},
				Spec: core.PersistentVolumeClaimSpec{
					StorageClassName: &scName,
					AccessModes:      []core.PersistentVolumeAccessMode{core.ReadWriteOnce},
					Resources: core.ResourceRequirements{
						Requests: core.ResourceList{
							core.ResourceStorage: resource.MustParse("100Mi"),
						},
					},
				},
			}},
			PodManagementPolicy: apps.ParallelPodManagement,
		},
	}
}

func (s *K8sBrokerSuite) TestEnsureOperator(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	configMapArg := &core.ConfigMap{
		ObjectMeta: v1.ObjectMeta{
			Name: "test-operator-config",
		},
		Data: map[string]string{
			"test-agent.conf": "agent-conf-data",
		},
	}
	statefulSetArg := operatorStatefulSetArg(1, "test-juju-operator-storage")

	gomock.InOrder(
		s.mockNamespaces.EXPECT().Update(&core.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test"}}).Times(1),
		s.mockStatefulSets.EXPECT().Get("juju-operator-test", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockConfigMaps.EXPECT().Update(configMapArg).Times(1),
		s.mockStorageClass.EXPECT().Get("test-juju-operator-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(&storagev1.StorageClass{ObjectMeta: v1.ObjectMeta{Name: "test-juju-operator-storage"}}, nil),
		s.mockStatefulSets.EXPECT().Update(statefulSetArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Create(statefulSetArg).Times(1).
			Return(nil, nil),
	)

	err := s.broker.EnsureOperator("test", "path/to/agent", &caas.OperatorConfig{
		OperatorImagePath: "/path/to/image",
		Version:           version.MustParse("2.99.0"),
		AgentConf:         []byte("agent-conf-data"),
		ResourceTags:      map[string]string{"fred": "mary"},
		CharmStorage: caas.CharmStorageParams{
			Size:         uint64(10),
			Provider:     "kubernetes",
			ResourceTags: map[string]string{"foo": "bar"},
		},
	})
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureOperatorNoAgentConfig(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	statefulSetArg := operatorStatefulSetArg(1, "test-juju-operator-storage")
	gomock.InOrder(
		s.mockNamespaces.EXPECT().Update(&core.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test"}}).Times(1),
		s.mockStatefulSets.EXPECT().Get("juju-operator-test", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockConfigMaps.EXPECT().Get("test-operator-config", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, nil),
		s.mockStorageClass.EXPECT().Get("test-juju-operator-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(&storagev1.StorageClass{ObjectMeta: v1.ObjectMeta{Name: "test-juju-operator-storage"}}, nil),
		s.mockStatefulSets.EXPECT().Update(statefulSetArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Create(statefulSetArg).Times(1).
			Return(nil, nil),
	)

	err := s.broker.EnsureOperator("test", "path/to/agent", &caas.OperatorConfig{
		OperatorImagePath: "/path/to/image",
		Version:           version.MustParse("2.99.0"),
		ResourceTags:      map[string]string{"fred": "mary"},
		CharmStorage: caas.CharmStorageParams{
			Size:         uint64(10),
			Provider:     "kubernetes",
			ResourceTags: map[string]string{"foo": "bar"},
		},
	})
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureOperatorNoAgentConfigMissingConfigMap(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	gomock.InOrder(
		s.mockNamespaces.EXPECT().Update(&core.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test"}}).Times(1),
		s.mockStatefulSets.EXPECT().Get("juju-operator-test", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockConfigMaps.EXPECT().Get("test-operator-config", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
	)

	err := s.broker.EnsureOperator("test", "path/to/agent", &caas.OperatorConfig{
		OperatorImagePath: "/path/to/image",
		Version:           version.MustParse("2.99.0"),
		CharmStorage: caas.CharmStorageParams{
			Size:     uint64(10),
			Provider: "kubernetes",
		},
	})
	c.Assert(err, gc.ErrorMatches, `config map for "test" should already exist:  "test" not found`)
}

func (s *K8sBrokerSuite) TestDeleteService(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	// Delete operations below return a not found to ensure it's treated as a no-op.
	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-test", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Delete("test", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Delete("test", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockDeployments.EXPECT().Delete("test", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
		s.mockPods.EXPECT().List(v1.ListOptions{LabelSelector: "juju-application==test"}).
			Return(&core.PodList{Items: []core.Pod{}}, nil),
		s.mockSecrets.EXPECT().List(v1.ListOptions{LabelSelector: "juju-application==test"}).Times(1).
			Return(&core.SecretList{Items: []core.Secret{{
				ObjectMeta: v1.ObjectMeta{Name: "secret"},
			}}}, nil),
		s.mockSecrets.EXPECT().Delete("secret", s.deleteOptions(v1.DeletePropagationForeground)).Times(1).
			Return(s.k8sNotFoundError()),
	)

	err := s.broker.DeleteService("test")
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureServiceNoUnits(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	two := int32(2)
	dc := &apps.Deployment{ObjectMeta: v1.ObjectMeta{Name: "juju-unit-storage"}, Spec: apps.DeploymentSpec{Replicas: &two}}
	zero := int32(0)
	emptyDc := dc
	emptyDc.Spec.Replicas = &zero
	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockDeployments.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(dc, nil),
		s.mockDeployments.EXPECT().Update(emptyDc).Times(1).
			Return(nil, nil),
	)

	params := &caas.ServiceParams{}
	err := s.broker.EnsureService("app-name", nil, params, 0, nil)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureServiceNoStorage(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	numUnits := int32(2)
	unitSpec, err := provider.MakeUnitSpec("app-name", "app-name", basicPodspec)
	c.Assert(err, jc.ErrorIsNil)
	podSpec := provider.PodSpec(unitSpec)

	deploymentArg := &appsv1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name: "app-name",
			Labels: map[string]string{
				"juju-application": "app-name",
				"fred":             "mary",
			}},
		Spec: appsv1.DeploymentSpec{
			Replicas: &numUnits,
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{"juju-application": "app-name"},
			},
			Template: core.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					GenerateName: "app-name-",
					Labels: map[string]string{
						"juju-application": "app-name",
						"fred":             "mary",
					},
				},
				Spec: podSpec,
			},
		},
	}
	serviceArg := &core.Service{
		ObjectMeta: v1.ObjectMeta{
			Name:        "app-name",
			Annotations: map[string]string{"a": "b"},
			Labels: map[string]string{
				"juju-application": "app-name",
				"fred":             "mary",
			}},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"juju-application": "app-name"},
			Type:     "nodeIP",
			Ports: []core.ServicePort{
				{Port: 80, TargetPort: intstr.FromInt(80), Protocol: "TCP"},
				{Port: 8080, Protocol: "TCP", Name: "fred"},
			},
			LoadBalancerIP: "10.0.0.1",
			ExternalName:   "ext-name",
		},
	}

	secretArg := s.secretArg(c, map[string]string{"fred": "mary"})
	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockSecrets.EXPECT().Update(secretArg).Times(1).
			Return(nil, nil),
		s.mockStatefulSets.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockDeployments.EXPECT().Update(deploymentArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockDeployments.EXPECT().Create(deploymentArg).Times(1).
			Return(nil, nil),
		s.mockServices.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Update(serviceArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Create(serviceArg).Times(1).
			Return(nil, nil),
	)

	params := &caas.ServiceParams{
		PodSpec:      basicPodspec,
		ResourceTags: map[string]string{"fred": "mary"},
	}
	err = s.broker.EnsureService("app-name", nil, params, 2, application.ConfigAttributes{
		"kubernetes-service-type":            "nodeIP",
		"kubernetes-service-loadbalancer-ip": "10.0.0.1",
		"kubernetes-service-externalname":    "ext-name",
		"kubernetes-service-annotations":     map[string]interface{}{"a": "b"},
	})
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureCustomResourceDefinitionCreate(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	podSpec := basicPodspec
	podSpec.CustomResourceDefinitions = []caas.CustomResourceDefinition{
		{
			Kind:    "TFJob",
			Group:   "kubeflow.org",
			Version: "v1alpha2",
			Scope:   "Namespaced",
			Validation: caas.CustomResourceDefinitionValidation{
				Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
					"tfReplicaSpecs": {
						Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
							"Worker": {
								Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
									"replicas": {
										Type:    "integer",
										Minimum: float64Ptr(1),
									},
								},
							},
							"PS": {
								Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
									"replicas": {
										Type: "integer", Minimum: float64Ptr(1),
									},
								},
							},
							"Chief": {
								Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
									"replicas": {
										Type:    "integer",
										Minimum: float64Ptr(1),
										Maximum: float64Ptr(1),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: v1.ObjectMeta{
			Name:      "tfjobs.kubeflow.org",
			Namespace: "test",
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   "kubeflow.org",
			Version: "v1alpha2",
			Scope:   "Namespaced",
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural:   "tfjobs",
				Kind:     "TFJob",
				Singular: "tfjob",
			},
			Validation: &apiextensionsv1beta1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1beta1.JSONSchemaProps{
					Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
						"tfReplicaSpecs": {
							Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
								"Worker": {
									Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
										"replicas": {
											Type:    "integer",
											Minimum: float64Ptr(1),
										},
									},
								},
								"PS": {
									Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
										"replicas": {
											Type: "integer", Minimum: float64Ptr(1),
										},
									},
								},
								"Chief": {
									Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
										"replicas": {
											Type:    "integer",
											Minimum: float64Ptr(1),
											Maximum: float64Ptr(1),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	gomock.InOrder(
		s.mockCustomResourceDefinition.EXPECT().Create(crd).Times(1).Return(crd, nil),
	)
	err := s.broker.EnsureCustomResourceDefinition("test", podSpec)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureCustomResourceDefinitionUpdate(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	podSpec := basicPodspec
	podSpec.CustomResourceDefinitions = []caas.CustomResourceDefinition{
		{
			Kind:    "TFJob",
			Group:   "kubeflow.org",
			Version: "v1alpha2",
			Scope:   "Namespaced",
			Validation: caas.CustomResourceDefinitionValidation{
				Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
					"tfReplicaSpecs": {
						Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
							"Worker": {
								Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
									"replicas": {
										Type:    "integer",
										Minimum: float64Ptr(1),
									},
								},
							},
							"PS": {
								Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
									"replicas": {
										Type: "integer", Minimum: float64Ptr(1),
									},
								},
							},
							"Chief": {
								Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
									"replicas": {
										Type:    "integer",
										Minimum: float64Ptr(1),
										Maximum: float64Ptr(1),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: v1.ObjectMeta{
			Name:      "tfjobs.kubeflow.org",
			Namespace: "test",
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   "kubeflow.org",
			Version: "v1alpha2",
			Scope:   "Namespaced",
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural:   "tfjobs",
				Kind:     "TFJob",
				Singular: "tfjob",
			},
			Validation: &apiextensionsv1beta1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1beta1.JSONSchemaProps{
					Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
						"tfReplicaSpecs": {
							Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
								"Worker": {
									Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
										"replicas": {
											Type:    "integer",
											Minimum: float64Ptr(1),
										},
									},
								},
								"PS": {
									Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
										"replicas": {
											Type: "integer", Minimum: float64Ptr(1),
										},
									},
								},
								"Chief": {
									Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
										"replicas": {
											Type:    "integer",
											Minimum: float64Ptr(1),
											Maximum: float64Ptr(1),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	gomock.InOrder(
		s.mockCustomResourceDefinition.EXPECT().Create(crd).Times(1).Return(crd, s.k8sAlreadyExistsError()),
		s.mockCustomResourceDefinition.EXPECT().Get("tfjobs.kubeflow.org", v1.GetOptions{}).Times(1).Return(crd, nil),
		s.mockCustomResourceDefinition.EXPECT().Update(crd).Times(1).Return(crd, nil),
	)
	err := s.broker.EnsureCustomResourceDefinition("test", podSpec)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureServiceWithStorage(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	unitSpec, err := provider.MakeUnitSpec("app-name", "app-name", basicPodspec)
	c.Assert(err, jc.ErrorIsNil)
	podSpec := provider.PodSpec(unitSpec)
	podSpec.Containers[0].VolumeMounts = []core.VolumeMount{{
		Name:      "database-0",
		MountPath: "path/to/here",
	}}
	statefulSetArg := unitStatefulSetArg(2, "juju-unit-storage", podSpec)

	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockSecrets.EXPECT().Update(s.secretArg(c, nil)).Times(1).
			Return(nil, nil),
		s.mockStorageClass.EXPECT().Get("test-juju-unit-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStorageClass.EXPECT().Get("juju-unit-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(&storagev1.StorageClass{ObjectMeta: v1.ObjectMeta{Name: "juju-unit-storage"}}, nil),
		s.mockStatefulSets.EXPECT().Update(statefulSetArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Create(statefulSetArg).Times(1).
			Return(nil, nil),
		s.mockServices.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Update(basicServiceArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Create(basicServiceArg).Times(1).
			Return(nil, nil),
	)

	params := &caas.ServiceParams{
		PodSpec: basicPodspec,
		Filesystems: []storage.KubernetesFilesystemParams{{
			StorageName: "database",
			Size:        100,
			Provider:    "kubernetes",
			Attachment: &storage.KubernetesFilesystemAttachmentParams{
				Path: "path/to/here",
			},
			ResourceTags: map[string]string{"foo": "bar"},
		}},
	}
	err = s.broker.EnsureService("app-name", nil, params, 2, application.ConfigAttributes{
		"kubernetes-service-type":            "nodeIP",
		"kubernetes-service-loadbalancer-ip": "10.0.0.1",
		"kubernetes-service-externalname":    "ext-name",
	})
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureServiceForDeploymentWithDevices(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	numUnits := int32(2)
	unitSpec, err := provider.MakeUnitSpec("app-name", "app-name", basicPodspec)
	c.Assert(err, jc.ErrorIsNil)
	podSpec := provider.PodSpec(unitSpec)
	podSpec.NodeSelector = map[string]string{"accelerator": "nvidia-tesla-p100"}
	for i := range podSpec.Containers {
		podSpec.Containers[i].Resources = core.ResourceRequirements{
			Limits: core.ResourceList{
				"nvidia.com/gpu": *resource.NewQuantity(3, resource.DecimalSI),
			},
			Requests: core.ResourceList{
				"nvidia.com/gpu": *resource.NewQuantity(3, resource.DecimalSI),
			},
		}
	}

	deploymentArg := &appsv1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:   "app-name",
			Labels: map[string]string{"juju-application": "app-name"}},
		Spec: appsv1.DeploymentSpec{
			Replicas: &numUnits,
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{"juju-application": "app-name"},
			},
			Template: core.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					GenerateName: "app-name-",
					Labels:       map[string]string{"juju-application": "app-name"},
				},
				Spec: podSpec,
			},
		},
	}

	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockSecrets.EXPECT().Update(s.secretArg(c, nil)).Times(1).
			Return(nil, nil),
		s.mockStatefulSets.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockDeployments.EXPECT().Update(deploymentArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockDeployments.EXPECT().Create(deploymentArg).Times(1).
			Return(nil, nil),
		s.mockServices.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Update(basicServiceArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Create(basicServiceArg).Times(1).
			Return(nil, nil),
	)

	params := &caas.ServiceParams{
		PodSpec: basicPodspec,
		Devices: []devices.KubernetesDeviceParams{
			{
				Type:       "nvidia.com/gpu",
				Count:      3,
				Attributes: map[string]string{"gpu": "nvidia-tesla-p100"},
			},
		},
	}
	err = s.broker.EnsureService("app-name", nil, params, 2, application.ConfigAttributes{
		"kubernetes-service-type":            "nodeIP",
		"kubernetes-service-loadbalancer-ip": "10.0.0.1",
		"kubernetes-service-externalname":    "ext-name",
	})
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureServiceForStatefulSetWithDevices(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	unitSpec, err := provider.MakeUnitSpec("app-name", "app-name", basicPodspec)
	c.Assert(err, jc.ErrorIsNil)
	podSpec := provider.PodSpec(unitSpec)
	podSpec.Containers[0].VolumeMounts = []core.VolumeMount{{
		Name:      "database-0",
		MountPath: "path/to/here",
	}}
	podSpec.NodeSelector = map[string]string{"accelerator": "nvidia-tesla-p100"}
	for i := range podSpec.Containers {
		podSpec.Containers[i].Resources = core.ResourceRequirements{
			Limits: core.ResourceList{
				"nvidia.com/gpu": *resource.NewQuantity(3, resource.DecimalSI),
			},
			Requests: core.ResourceList{
				"nvidia.com/gpu": *resource.NewQuantity(3, resource.DecimalSI),
			},
		}
	}
	statefulSetArg := unitStatefulSetArg(2, "juju-unit-storage", podSpec)

	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockSecrets.EXPECT().Update(s.secretArg(c, nil)).Times(1).
			Return(nil, nil),
		s.mockStorageClass.EXPECT().Get("test-juju-unit-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStorageClass.EXPECT().Get("juju-unit-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(&storagev1.StorageClass{ObjectMeta: v1.ObjectMeta{Name: "juju-unit-storage"}}, nil),
		s.mockStatefulSets.EXPECT().Update(statefulSetArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Create(statefulSetArg).Times(1).
			Return(nil, nil),
		s.mockServices.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Update(basicServiceArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Create(basicServiceArg).Times(1).
			Return(nil, nil),
	)

	params := &caas.ServiceParams{
		PodSpec: basicPodspec,
		Filesystems: []storage.KubernetesFilesystemParams{{
			StorageName: "database",
			Size:        100,
			Provider:    "kubernetes",
			Attachment: &storage.KubernetesFilesystemAttachmentParams{
				Path: "path/to/here",
			},
			ResourceTags: map[string]string{"foo": "bar"},
		}},
		Devices: []devices.KubernetesDeviceParams{
			{
				Type:       "nvidia.com/gpu",
				Count:      3,
				Attributes: map[string]string{"gpu": "nvidia-tesla-p100"},
			},
		},
	}
	err = s.broker.EnsureService("app-name", nil, params, 2, application.ConfigAttributes{
		"kubernetes-service-type":            "nodeIP",
		"kubernetes-service-loadbalancer-ip": "10.0.0.1",
		"kubernetes-service-externalname":    "ext-name",
	})
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureServiceWithConstraints(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	unitSpec, err := provider.MakeUnitSpec("app-name", "app-name", basicPodspec)
	c.Assert(err, jc.ErrorIsNil)
	podSpec := provider.PodSpec(unitSpec)
	podSpec.Containers[0].VolumeMounts = []core.VolumeMount{{
		Name:      "database-0",
		MountPath: "path/to/here",
	}}
	for i := range podSpec.Containers {
		podSpec.Containers[i].Resources = core.ResourceRequirements{
			Limits: core.ResourceList{
				"memory": resource.MustParse("64Mi"),
				"cpu":    resource.MustParse("500m"),
			},
		}
	}
	statefulSetArg := unitStatefulSetArg(2, "juju-unit-storage", podSpec)

	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockSecrets.EXPECT().Update(s.secretArg(c, nil)).Times(1).
			Return(nil, nil),
		s.mockStorageClass.EXPECT().Get("test-juju-unit-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStorageClass.EXPECT().Get("juju-unit-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(&storagev1.StorageClass{ObjectMeta: v1.ObjectMeta{Name: "juju-unit-storage"}}, nil),
		s.mockStatefulSets.EXPECT().Update(statefulSetArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Create(statefulSetArg).Times(1).
			Return(nil, nil),
		s.mockServices.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Update(basicServiceArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Create(basicServiceArg).Times(1).
			Return(nil, nil),
	)

	params := &caas.ServiceParams{
		PodSpec: basicPodspec,
		Filesystems: []storage.KubernetesFilesystemParams{{
			StorageName: "database",
			Size:        100,
			Provider:    "kubernetes",
			Attachment: &storage.KubernetesFilesystemAttachmentParams{
				Path: "path/to/here",
			},
			ResourceTags: map[string]string{"foo": "bar"},
		}},
		Constraints: constraints.MustParse("mem=64 cpu-power=500"),
	}
	err = s.broker.EnsureService("app-name", nil, params, 2, application.ConfigAttributes{
		"kubernetes-service-type":            "nodeIP",
		"kubernetes-service-loadbalancer-ip": "10.0.0.1",
		"kubernetes-service-externalname":    "ext-name",
	})
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestEnsureServiceWithPlacement(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	unitSpec, err := provider.MakeUnitSpec("app-name", "app-name", basicPodspec)
	c.Assert(err, jc.ErrorIsNil)
	podSpec := provider.PodSpec(unitSpec)
	podSpec.Containers[0].VolumeMounts = []core.VolumeMount{{
		Name:      "database-0",
		MountPath: "path/to/here",
	}}
	podSpec.NodeSelector = map[string]string{"a": "b"}
	statefulSetArg := unitStatefulSetArg(2, "juju-unit-storage", podSpec)

	gomock.InOrder(
		s.mockStatefulSets.EXPECT().Get("juju-operator-app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockSecrets.EXPECT().Update(s.secretArg(c, nil)).Times(1).
			Return(nil, nil),
		s.mockStorageClass.EXPECT().Get("test-juju-unit-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStorageClass.EXPECT().Get("juju-unit-storage", v1.GetOptions{IncludeUninitialized: false}).Times(1).
			Return(&storagev1.StorageClass{ObjectMeta: v1.ObjectMeta{Name: "juju-unit-storage"}}, nil),
		s.mockStatefulSets.EXPECT().Update(statefulSetArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockStatefulSets.EXPECT().Create(statefulSetArg).Times(1).
			Return(nil, nil),
		s.mockServices.EXPECT().Get("app-name", v1.GetOptions{IncludeUninitialized: true}).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Update(basicServiceArg).Times(1).
			Return(nil, s.k8sNotFoundError()),
		s.mockServices.EXPECT().Create(basicServiceArg).Times(1).
			Return(nil, nil),
	)

	params := &caas.ServiceParams{
		PodSpec: basicPodspec,
		Filesystems: []storage.KubernetesFilesystemParams{{
			StorageName: "database",
			Size:        100,
			Provider:    "kubernetes",
			Attachment: &storage.KubernetesFilesystemAttachmentParams{
				Path: "path/to/here",
			},
			ResourceTags: map[string]string{"foo": "bar"},
		}},
		Placement: "a=b",
	}
	err = s.broker.EnsureService("app-name", nil, params, 2, application.ConfigAttributes{
		"kubernetes-service-type":            "nodeIP",
		"kubernetes-service-loadbalancer-ip": "10.0.0.1",
		"kubernetes-service-externalname":    "ext-name",
	})
	c.Assert(err, jc.ErrorIsNil)
}

func (s *K8sBrokerSuite) TestOperator(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	opPod := core.Pod{
		ObjectMeta: v1.ObjectMeta{
			Name: "test-operator",
		},
		Status: core.PodStatus{
			Phase:   core.PodPending,
			Message: "test message.",
		},
	}
	gomock.InOrder(
		s.mockPods.EXPECT().List(v1.ListOptions{LabelSelector: "juju-operator==test"}).Times(1).
			Return(&core.PodList{Items: []core.Pod{opPod}}, nil),
	)

	operator, err := s.broker.Operator("test")
	c.Assert(err, jc.ErrorIsNil)

	c.Assert(operator.Status.Status, gc.Equals, status.Allocating)
	c.Assert(operator.Status.Message, gc.Equals, "test message.")
}

func (s *K8sBrokerSuite) TestOperatorNoPodFound(c *gc.C) {
	ctrl := s.setupBroker(c)
	defer ctrl.Finish()

	gomock.InOrder(
		s.mockPods.EXPECT().List(v1.ListOptions{LabelSelector: "juju-operator==test"}).Times(1).
			Return(&core.PodList{Items: []core.Pod{}}, nil),
	)

	_, err := s.broker.Operator("test")
	c.Assert(err, gc.ErrorMatches, "operator pod for application \"test\" not found")
}
