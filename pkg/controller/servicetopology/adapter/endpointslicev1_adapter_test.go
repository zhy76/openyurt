/*
Copyright 2022 The OpenYurt Authors.
Copyright 2017 The Kubernetes Authors.

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

package adapter

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEndpointSliceV1AdapterUpdateTriggerAnnotations(t *testing.T) {
	svcName := "svc1"
	svcNamespace := "default"
	epSlice := getEndpointSlice(svcNamespace, svcName, "node1")

	kubeClient := fake.NewSimpleClientset(epSlice)
	c := fakeclient.NewClientBuilder().WithObjects(epSlice).Build()
	stopper := make(chan struct{})
	defer close(stopper)
	adapter := NewEndpointsV1Adapter(kubeClient, c)
	err := adapter.UpdateTriggerAnnotations(epSlice.Namespace, epSlice.Name)
	if err != nil {
		t.Errorf("update endpointsSlice trigger annotations failed")
	}

	newEpSlice, err := kubeClient.DiscoveryV1().EndpointSlices(epSlice.Namespace).Get(context.TODO(), epSlice.Name, metav1.GetOptions{})
	if err != nil || epSlice.Annotations["openyurt.io/update-trigger"] == newEpSlice.Annotations["openyurt.io/update-trigger"] {
		t.Errorf("update endpoints trigger annotations failed")
	}
}

func TestEndpointSliceV1AdapterGetEnqueueKeysBySvc(t *testing.T) {
	svcName := "svc1"
	svcNamespace := "default"
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: svcNamespace,
		},
	}
	epSlice := getEndpointSlice(svcNamespace, svcName, "node1")
	expectResult := []string{getCacheKey(epSlice)}

	stopper := make(chan struct{})
	defer close(stopper)
	kubeClient := fake.NewSimpleClientset(epSlice)
	c := fakeclient.NewClientBuilder().WithObjects(epSlice).Build()
	adapter := NewEndpointsV1Adapter(kubeClient, c)

	keys := adapter.GetEnqueueKeysBySvc(svc)
	if !reflect.DeepEqual(keys, expectResult) {
		t.Errorf("expect enqueue keys %v, but got %v", expectResult, keys)
	}
}

func getEndpointSlice(svcNamespace, svcName string, nodes ...string) *discoveryv1.EndpointSlice {
	var endpoints []discoveryv1.Endpoint
	for i := range nodes {
		endpoints = append(endpoints, discoveryv1.Endpoint{NodeName: &nodes[i]})
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-xad21", svcName),
			Namespace: svcNamespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: svcName,
			},
		},
		Endpoints: endpoints,
	}
}
