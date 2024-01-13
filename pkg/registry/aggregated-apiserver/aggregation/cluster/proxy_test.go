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
	"github.com/kubewharf/kubeadmiral/pkg/util/mock"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	apis "github.com/kubewharf/kubeadmiral/pkg/apis/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
)

func TestProxyREST_Connect(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/proxy" {
			_, _ = io.WriteString(rw, "ok")
		} else {
			_, _ = io.WriteString(rw, "bad request: "+req.URL.Path)
		}
	}))
	defer s.Close()

	type fields struct {
		clusterGetter func(ctx context.Context, name string) (*apis.FederatedCluster, error)
	}
	type args struct {
		id      string
		options runtime.Object
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    string
		wantErr bool
	}{
		{
			name: "options is invalid",
			fields: fields{
				clusterGetter: func(_ context.Context, name string) (*apis.FederatedCluster, error) {
					return &apis.FederatedCluster{
						ObjectMeta: metav1.ObjectMeta{Name: name},
						Spec: apis.FederatedClusterSpec{
							APIEndpoint: s.URL,
							Insecure:    true,
						},
					}, nil
				},
			},
			args: args{
				id:      "cluster",
				options: &corev1.Pod{},
			},
			wantErr: true,
			want:    "",
		},
		{
			name: "cluster not found",
			fields: fields{
				clusterGetter: func(_ context.Context, name string) (*apis.FederatedCluster, error) {
					return nil, apierrors.NewNotFound(apis.Resource("federatedclusters"), name)
				},
			},
			args: args{
				id:      "cluster",
				options: &apis.ClusterProxyOptions{Path: "/proxy"},
			},
			wantErr: true,
			want:    "",
		},
		{
			name: "proxy success",
			fields: fields{
				clusterGetter: func(_ context.Context, name string) (*apis.FederatedCluster, error) {
					return &apis.FederatedCluster{
						ObjectMeta: metav1.ObjectMeta{Name: name},
						Spec: apis.FederatedClusterSpec{
							APIEndpoint: s.URL,
							Insecure:    true,
						},
					}, nil
				},
			},
			args: args{
				id:      "cluster",
				options: &apis.ClusterProxyOptions{Path: "/proxy"},
			},
			wantErr: false,
			want:    "ok",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stopCh := make(chan struct{})
			defer close(stopCh)

			req, err := http.NewRequestWithContext(request.WithUser(request.NewContext(), &user.DefaultInfo{}), http.MethodGet, "http://127.0.0.1/xxx", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp := httptest.NewRecorder()

			r := &ProxyREST{
				clusterGetter: tt.fields.clusterGetter,
			}

			h, err := r.Connect(req.Context(), tt.args.id, tt.args.options, mock.NewResponder(resp))
			if (err != nil) != tt.wantErr {
				t.Errorf("Connect() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			h.ServeHTTP(resp, req)
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Error(err)
				return
			}
			if got := string(body); got != tt.want {
				t.Errorf("Connect() got = %v, want %v", got, tt.want)
			}
		})
	}
}
