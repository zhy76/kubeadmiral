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
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	restclient "k8s.io/client-go/rest"
	"net/http"
	"net/url"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
	kubeclient "k8s.io/client-go/kubernetes"

	coreapis "github.com/kubewharf/kubeadmiral/pkg/apis/core/v1alpha1"
	fedclient "github.com/kubewharf/kubeadmiral/pkg/client/clientset/versioned"
)

const matchAllClusters = "*"

// ProxyREST implements the proxy subresource for a Cluster.
type ProxyREST struct {
	Redirector    rest.Redirector
	restConfig    *restclient.Config
	kubeClient    kubeclient.Interface
	fedClientset  fedclient.Interface
	resolver      genericapirequest.RequestInfoResolver
	clusterGetter func(ctx context.Context, name string) (*coreapis.FederatedCluster, error)
	clusterLister func(ctx context.Context) (*coreapis.FederatedClusterList, error)

	admiralLocation  *url.URL
	admiralTransPort http.RoundTripper
}

// Implement Connecter
var _ = rest.Connecter(&ProxyREST{})

var proxyMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// New returns an empty cluster proxy subresource.
func (r *ProxyREST) New() runtime.Object {
	return &coreapis.ClusterProxyOptions{}
}

// ConnectMethods returns the list of HTTP methods handled by Connect.
func (r *ProxyREST) ConnectMethods() []string {
	return proxyMethods
}

// NewConnectOptions returns versioned resource that represents proxy parameters.
func (r *ProxyREST) NewConnectOptions() (runtime.Object, bool, string) {
	return &coreapis.ClusterProxyOptions{}, true, "path"
}

// Connect returns a handler for the cluster proxy.
func (r *ProxyREST) Connect(ctx context.Context, id string, options runtime.Object, responder rest.Responder) (http.Handler, error) {
	proxyOpts, ok := options.(*coreapis.ClusterProxyOptions)
	if !ok {
		return nil, fmt.Errorf("invalid options object: %#v", options)
	}

	if id == matchAllClusters {
		return r.connectAllClusters(ctx, proxyOpts.Path, responder)
	}

	cluster, err := r.clusterGetter(ctx, id)
	if err != nil {
		return nil, err
	}

	return proxy.ConnectCluster(ctx, cluster, proxyOpts.Path, responder)
}

// Destroy cleans up its resources on shutdown.
func (r *ProxyREST) Destroy() {
	// Given no underlying store, so we don't
	// need to destroy anything.
}
