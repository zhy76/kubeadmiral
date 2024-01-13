/*
Copyright 2024 The KubeAdmiral Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cluster

import (
	"context"
	"fmt"
	"github.com/kubewharf/kubeadmiral/pkg/util/proxy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"net/http"
	"net/url"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"

	coreapis "github.com/kubewharf/kubeadmiral/pkg/apis/core/v1alpha1"
	"github.com/kubewharf/kubeadmiral/pkg/registry/aggregated-apiserver/aggregation"
)

// ClusterStorage includes storage for FederatedCluster and for Status subresource.
type ClusterStorage struct {
	Cluster *REST
	Status  *StatusREST
	Proxy   *ProxyREST
}

// NewStorage returns new instance of ClusterStorage.
func NewStorage(scheme *runtime.Scheme, restConfig *restclient.Config, optsGetter generic.RESTOptionsGetter) (*ClusterStorage, error) {
	clusterRest, clusterStatusRest, ProxyRest, err := NewREST(scheme, restConfig, optsGetter)
	if err != nil {
		return &ClusterStorage{}, err
	}

	return &ClusterStorage{
		Cluster: clusterRest,
		Status:  clusterStatusRest,
		Proxy:   ProxyRest,
	}, nil
}

// NewREST returns a RESTStorage object that will work against API services.
func NewREST(scheme *runtime.Scheme, restConfig *restclient.Config, optsGetter generic.RESTOptionsGetter) (*REST, *StatusREST, *ProxyREST, error) {
	strategy := aggregation.NewStrategy(scheme)

	store := &genericregistry.Store{
		NewFunc:                  func() runtime.Object { return &coreapis.FederatedCluster{} },
		NewListFunc:              func() runtime.Object { return &coreapis.FederatedClusterList{} },
		PredicateFunc:            aggregation.MatchCluster,
		DefaultQualifiedResource: coreapis.Resource("federatedclusters"),

		CreateStrategy:      strategy,
		UpdateStrategy:      strategy,
		DeleteStrategy:      strategy,
		ResetFieldsStrategy: strategy,

		// TODO: define table converter that exposes more than name/creation timestamp
		TableConvertor: rest.NewDefaultTableConvertor(coreapis.Resource("federatedclusters")),
	}

	options := &generic.StoreOptions{RESTOptions: optsGetter, AttrFunc: aggregation.GetAttrs}
	if err := store.CompleteWithOptions(options); err != nil {
		return nil, nil, nil, err
	}

	statusStrategy := aggregation.NewStatusStrategy(strategy)
	statusStore := *store
	statusStore.UpdateStrategy = statusStrategy
	statusStore.ResetFieldsStrategy = statusStrategy

	kubeClientSet := kubernetes.NewForConfigOrDie(restConfig)
	admiralLocation, admiralTransport, err := resourceLocation(restConfig)
	if err != nil {
		return nil, nil, nil, err
	}
	clusterRest := &REST{store}
	return clusterRest,
		&StatusREST{store: &statusStore},
		&ProxyREST{
			restConfig:       restConfig,
			kubeClient:       kubeClientSet,
			clusterGetter:    clusterRest.getCluster,
			clusterLister:    clusterRest.listClusters,
			admiralLocation:  admiralLocation,
			admiralTransPort: admiralTransport}, nil
}

// REST implements a RESTStorage for Cluster.
type REST struct {
	*genericregistry.Store
}

// Implement Redirector.
var _ = rest.Redirector(&REST{})

// ResourceLocation returns a URL to which one can send traffic for the specified cluster.
func (r *REST) ResourceLocation(ctx context.Context, name string) (*url.URL, http.RoundTripper, error) {
	cluster, err := r.getCluster(ctx, name)
	if err != nil {
		return nil, nil, err
	}

	tlsConfig, err := proxy.GetTlsConfigForCluster(ctx, cluster)
	if err != nil {
		return nil, nil, err
	}

	return proxy.Location(cluster, tlsConfig)
}

func (r *REST) getCluster(ctx context.Context, name string) (*coreapis.FederatedCluster, error) {
	obj, err := r.Get(ctx, name, &metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	cluster := obj.(*coreapis.FederatedCluster)
	if cluster == nil {
		return nil, fmt.Errorf("unexpected object type: %#v", obj)
	}
	return cluster, nil
}

func (r *REST) listClusters(ctx context.Context) (*coreapis.FederatedClusterList, error) {
	obj, err := r.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	clusterList := obj.(*coreapis.FederatedClusterList)
	if clusterList == nil {
		return nil, fmt.Errorf("unexpected object type: %#v", obj)
	}
	return clusterList, nil
}

// ResourceGetter is an interface for retrieving resources by ResourceLocation.
type ResourceGetter interface {
	Get(context.Context, string, *metav1.GetOptions) (runtime.Object, error)
}

// StatusREST implements the REST endpoint for changing the status of a cluster.
type StatusREST struct {
	store *genericregistry.Store
}

func (r *StatusREST) Destroy() {
}

// New returns empty Cluster object.
func (r *StatusREST) New() runtime.Object {
	return &coreapis.FederatedCluster{}
}

// Get retrieves the object from the storage. It is required to support Patch.
func (r *StatusREST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	return r.store.Get(ctx, name, options)
}

// Update alters the status subset of an object.
func (r *StatusREST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	// We are explicitly setting forceAllowCreate to false in the call to the underlying storage because
	// subresources should never allow create on update.
	return r.store.Update(ctx, name, objInfo, createValidation, updateValidation, false, options)
}

// GetResetFields implements rest.ResetFieldsStrategy
func (r *StatusREST) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return r.store.GetResetFields()
}
