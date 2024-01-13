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

package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/proxy"
	proxyutil "k8s.io/apimachinery/pkg/util/proxy"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	registryrest "k8s.io/apiserver/pkg/registry/rest"

	apis "github.com/kubewharf/kubeadmiral/pkg/apis/core/v1alpha1"
)

type SecretGetterFunc func(context.Context, string) (*corev1.Secret, error)

// ConnectCluster returns a handler for proxy cluster.
func ConnectCluster(ctx context.Context, cluster *apis.FederatedCluster, proxyPath string, responder registryrest.Responder) (http.Handler, error) {
	tlsConfig, err := GetTlsConfigForCluster(ctx, cluster)
	if err != nil {
		return nil, err
	}

	// In the Location function, the tlsConfig.NextProtos will be modified,
	// which will affect its usage in the newProxyHandler function (e.g., exec requires an upgraded tls connection).
	// Therefore, we clone the tlsConfig here to prevent any unexpected modifications.
	// TODO: Identify the root cause and find a better solution to fix it.
	location, proxyTransport, err := Location(cluster, tlsConfig.Clone())
	if err != nil {
		return nil, err
	}
	location.Path = path.Join(location.Path, proxyPath)

	return newProxyHandler(location, proxyTransport, cluster, responder, tlsConfig.Clone())
}

func newProxyHandler(location *url.URL, proxyTransport http.RoundTripper, cluster *apis.FederatedCluster,
	responder registryrest.Responder, tlsConfig *tls.Config) (http.Handler, error) {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		requester, exist := request.UserFrom(req.Context())
		if !exist {
			responsewriters.InternalError(rw, req, errors.New("no user found for request"))
			return
		}

		req.Header.Set(authenticationv1.ImpersonateUserHeader, requester.GetName())
		for _, group := range requester.GetGroups() {
			if !SkipGroup(group) {
				req.Header.Add(authenticationv1.ImpersonateGroupHeader, group)
			}
		}

		var proxyURL *url.URL

		// Retain RawQuery in location because upgrading the request will use it.
		// See https://github.com/karmada-io/karmada/issues/1618#issuecomment-1103793290 for more info.
		location.RawQuery = req.URL.RawQuery

		upgradeDialer := NewUpgradeDialerWithConfig(UpgradeDialerWithConfig{
			TLS:        tlsConfig,
			Proxier:    http.ProxyURL(proxyURL),
			PingPeriod: time.Second * 5,
		})

		handler := NewUpgradeAwareHandler(location, proxyTransport, false, httpstream.IsUpgradeRequest(req), proxyutil.NewErrorResponder(responder))
		handler.UpgradeDialer = upgradeDialer
		handler.ServeHTTP(rw, req)
	}), nil
}

// NewThrottledUpgradeAwareProxyHandler creates a new proxy handler with a default flush interval. Responder is required for returning
// errors to the caller.
func NewThrottledUpgradeAwareProxyHandler(location *url.URL, transport http.RoundTripper, wrapTransport, upgradeRequired bool, responder registryrest.Responder) *proxy.UpgradeAwareHandler {
	return proxy.NewUpgradeAwareHandler(location, transport, wrapTransport, upgradeRequired, proxy.NewErrorResponder(responder))
}

func GetTlsConfigForCluster(ctx context.Context, cluster *apis.FederatedCluster) (*tls.Config, error) {
	// The secret is optional for a pull-mode cluster, if no secret just returns a config with root CA unset.
	if cluster.Spec.SecretRef.Name == "" {
		return &tls.Config{
			MinVersion: tls.VersionTLS13,
			// Ignore false positive warning: "TLS InsecureSkipVerify may be true. (gosec)"
			// Whether to skip server certificate verification depends on the
			// configuration(.spec.insecureSkipTLSVerification, defaults to false) in a Cluster object.
			InsecureSkipVerify: cluster.Spec.Insecure, //nolint:gosec
		}, nil
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Ignore false positive warning: "TLS InsecureSkipVerify may be true. (gosec)"
		// Whether to skip server certificate verification depends on the
		// configuration(.spec.insecureSkipTLSVerification, defaults to false) in a Cluster object.
		InsecureSkipVerify: cluster.Spec.Insecure, //nolint:gosec
	}, nil
}

// Location returns a URL to which one can send traffic for the specified cluster.
func Location(cluster *apis.FederatedCluster, tlsConfig *tls.Config) (*url.URL, http.RoundTripper, error) {
	location, err := constructLocation(cluster)
	if err != nil {
		return nil, nil, err
	}

	proxyTransport, err := createProxyTransport(cluster, tlsConfig)
	if err != nil {
		return nil, nil, err
	}

	return location, proxyTransport, nil
}

func constructLocation(cluster *apis.FederatedCluster) (*url.URL, error) {
	apiEndpoint := cluster.Spec.APIEndpoint
	if apiEndpoint == "" {
		return nil, fmt.Errorf("API endpoint of cluster %s should not be empty", cluster.GetName())
	}

	uri, err := url.Parse(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse api endpoint %s: %v", apiEndpoint, err)
	}
	return uri, nil
}

func createProxyTransport(cluster *apis.FederatedCluster, tlsConfig *tls.Config) (*http.Transport, error) {
	var proxyDialerFn utilnet.DialFunc
	trans := utilnet.SetTransportDefaults(&http.Transport{
		DialContext:     proxyDialerFn,
		TLSClientConfig: tlsConfig,
	})

	return trans, nil
}

// SkipGroup tells whether the input group can be skipped during impersonate.
func SkipGroup(group string) bool {
	switch group {
	case user.AllAuthenticated, user.AllUnauthenticated:
		return true
	default:
		return false
	}
}
