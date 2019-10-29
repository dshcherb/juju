// Copyright 2019 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package specs

import (
	"fmt"
	"strings"

	"github.com/juju/errors"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/juju/juju/caas/specs"
)

type podSpecV2 struct {
	caaSSpec specs.PodSpecV2
	k8sSpec  K8sPodSpecV2
}

// Validate is defined on ProviderPod.
func (p podSpecV2) Validate() error {
	if err := p.caaSSpec.Validate(); err != nil {
		return errors.Trace(err)
	}
	if err := p.k8sSpec.Validate(); err != nil {
		return errors.Trace(err)
	}
	return nil
}

func (p podSpecV2) ToLatest() *specs.PodSpec {
	pSpec := &specs.PodSpec{}
	pSpec.Version = specs.CurrentVersion
	// TOD(caas): OmitServiceFrontend is deprecated in v2 and will be removed in v3.
	pSpec.OmitServiceFrontend = false
	pSpec.Containers = p.caaSSpec.Containers
	pSpec.Service = p.caaSSpec.Service
	pSpec.ConfigMaps = p.caaSSpec.ConfigMaps
	pSpec.ServiceAccount = p.caaSSpec.ServiceAccount
	pSpec.ProviderPod = &p.k8sSpec
	return pSpec
}

// K8sPodSpecV2 is a subset of v1.PodSpec which defines
// attributes we expose for charms to set.
type K8sPodSpecV2 struct {
	// k8s resources.
	KubernetesResources *KubernetesResources `json:"kubernetesResources,omitempty"`
}

// Validate is defined on ProviderPod.
func (p *K8sPodSpecV2) Validate() error {
	if p.KubernetesResources != nil {
		if err := p.KubernetesResources.Validate(); err != nil {
			return errors.Trace(err)
		}
	}
	return nil
}

// K8sServiceAccountSpec defines spec for referencing or creating a service account.
type K8sServiceAccountSpec struct {
	Name           string `yaml:"name" json:"name"`
	specs.RBACSpec `yaml:",inline"`
}

// GetName returns the service accout name.
func (sa K8sServiceAccountSpec) GetName() string {
	return sa.Name
}

// GetSpec returns the RBAC spec.
func (sa K8sServiceAccountSpec) GetSpec() specs.RBACSpec {
	return sa.RBACSpec
}

// Validate returns an error if the spec is not valid.
func (sa K8sServiceAccountSpec) Validate() error {
	if sa.Name == "" {
		return errors.New("service account name is missing")
	}
	return errors.Trace(sa.RBACSpec.Validate())
}

// KubernetesResources is the k8s related resources.
type KubernetesResources struct {
	Pod *PodSpec `json:"pod,omitempty"`

	Secrets                   []Secret                                                     `json:"secrets" yaml:"secrets"`
	CustomResourceDefinitions map[string]apiextensionsv1beta1.CustomResourceDefinitionSpec `json:"customResourceDefinitions,omitempty" yaml:"customResourceDefinitions,omitempty"`
	CustomResources           map[string][]unstructured.Unstructured                       `json:"customResources,omitempty" yaml:"customResources,omitempty"`

	ServiceAccounts []K8sServiceAccountSpec `json:"serviceAccounts,omitempty" yaml:"serviceAccounts,omitempty"`
}

// Validate is defined on ProviderPod.
func (krs *KubernetesResources) Validate() error {
	for k, crd := range krs.CustomResourceDefinitions {
		if crd.Scope != apiextensionsv1beta1.NamespaceScoped {
			return errors.NewNotSupported(nil,
				fmt.Sprintf("custom resource definition %q scope %q is not supported, please use %q scope",
					k, crd.Scope, apiextensionsv1beta1.NamespaceScoped),
			)
		}
	}

	for k, crs := range krs.CustomResources {
		if _, ok := krs.CustomResourceDefinitions[k]; !ok {
			return errors.NewNotValid(nil, fmt.Sprintf("no custom resource definition found for custom resource %q", k))
		}
		if len(crs) == 0 {
			return errors.NotValidf("empty custom resources %q", k)
		}
	}

	for _, sa := range krs.ServiceAccounts {
		if err := sa.Validate(); err != nil {
			return errors.Trace(err)
		}
	}
	return nil
}

func parsePodSpecV2(in string) (_ PodSpecConverter, err error) {
	// Do the common fields.
	var spec podSpecV2

	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(in), len(in))
	if err = decoder.Decode(&spec.caaSSpec); err != nil {
		return nil, errors.Trace(err)
	}

	// Do the k8s pod attributes.
	decoder = k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(in), len(in))
	if err = decoder.Decode(&spec.k8sSpec); err != nil {
		return nil, errors.Trace(err)
	}

	// Do the k8s containers.
	var containers k8sContainers
	if err := parseContainers(in, &containers); err != nil {
		return nil, errors.Trace(err)
	}

	// Compose the result.
	for i, c := range containers.Containers {
		if err = c.Validate(); err != nil {
			return nil, errors.Trace(err)
		}
		spec.caaSSpec.Containers[i] = c.ToContainerSpec()
	}
	return &spec, nil
}
