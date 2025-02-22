/*
Copyright 2023 The OpenYurt Authors.

Licensed under the Apache License, Version 2.0 (the License);
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an AS IS BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package platformadmin

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	appconfig "github.com/openyurtio/openyurt/cmd/yurt-manager/app/config"
	appsv1alpha1 "github.com/openyurtio/openyurt/pkg/apis/apps/v1alpha1"
	iotv1alpha1 "github.com/openyurtio/openyurt/pkg/apis/iot/v1alpha1"
	iotv1alpha2 "github.com/openyurtio/openyurt/pkg/apis/iot/v1alpha2"
	"github.com/openyurtio/openyurt/pkg/controller/platformadmin/config"
	util "github.com/openyurtio/openyurt/pkg/controller/platformadmin/utils"
	utilclient "github.com/openyurtio/openyurt/pkg/util/client"
	utildiscovery "github.com/openyurtio/openyurt/pkg/util/discovery"
)

func init() {
	flag.IntVar(&concurrentReconciles, "platformadmin-workers", concurrentReconciles, "Max concurrent workers for PlatformAdmin controller.")
}

var (
	concurrentReconciles = 3
	controllerKind       = iotv1alpha2.SchemeGroupVersion.WithKind("PlatformAdmin")
)

const (
	ControllerName = "PlatformAdmin"

	LabelConfigmap  = "Configmap"
	LabelService    = "Service"
	LabelDeployment = "Deployment"

	AnnotationServiceTopologyKey           = "openyurt.io/topologyKeys"
	AnnotationServiceTopologyValueNodePool = "openyurt.io/nodepool"

	ConfigMapName = "common-variables"
)

func Format(format string, args ...interface{}) string {
	s := fmt.Sprintf(format, args...)
	return fmt.Sprintf("%s: %s", ControllerName, s)
}

// ReconcilePlatformAdmin reconciles a PlatformAdmin object
type ReconcilePlatformAdmin struct {
	client.Client
	scheme       *runtime.Scheme
	recorder     record.EventRecorder
	Configration config.PlatformAdminControllerConfiguration
}

var _ reconcile.Reconciler = &ReconcilePlatformAdmin{}

// Add creates a new PlatformAdmin Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(c *appconfig.CompletedConfig, mgr manager.Manager) error {
	if !utildiscovery.DiscoverGVK(controllerKind) {
		return nil
	}

	klog.Infof("platformadmin-controller add controller %s", controllerKind.String())
	return add(mgr, newReconciler(c, mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(c *appconfig.CompletedConfig, mgr manager.Manager) reconcile.Reconciler {
	return &ReconcilePlatformAdmin{
		Client:       utilclient.NewClientFromManager(mgr, ControllerName),
		scheme:       mgr.GetScheme(),
		recorder:     mgr.GetEventRecorderFor(ControllerName),
		Configration: c.ComponentConfig.PlatformAdminController,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New(ControllerName, mgr, controller.Options{
		Reconciler: r, MaxConcurrentReconciles: concurrentReconciles,
	})
	if err != nil {
		return err
	}

	// Watch for changes to PlatformAdmin
	err = c.Watch(&source.Kind{Type: &iotv1alpha2.PlatformAdmin{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForOwner{
		IsController: false,
		OwnerType:    &iotv1alpha2.PlatformAdmin{},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Service{}}, &handler.EnqueueRequestForOwner{
		IsController: false,
		OwnerType:    &iotv1alpha2.PlatformAdmin{},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &appsv1alpha1.YurtAppSet{}}, &handler.EnqueueRequestForOwner{
		IsController: false,
		OwnerType:    &iotv1alpha2.PlatformAdmin{},
	})
	if err != nil {
		return err
	}

	klog.V(4).Info("registering the field indexers of platformadmin controller")
	if err := util.RegisterFieldIndexers(mgr.GetFieldIndexer()); err != nil {
		klog.Errorf("failed to register field indexers for platformadmin controller, %v", err)
		return nil
	}

	return nil
}

// +kubebuilder:rbac:groups=iot.openyurt.io,resources=platformadmins,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=iot.openyurt.io,resources=platformadmins/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=iot.openyurt.io,resources=platformadmins/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps.openyurt.io,resources=yurtappsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.openyurt.io,resources=yurtappsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=configmaps;services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps/status;services/status,verbs=get;update;patch

// Reconcile reads that state of the cluster for a PlatformAdmin object and makes changes based on the state read
// and what is in the PlatformAdmin.Spec
func (r *ReconcilePlatformAdmin) Reconcile(ctx context.Context, request reconcile.Request) (_ reconcile.Result, reterr error) {
	klog.Infof(Format("Reconcile PlatformAdmin %s/%s", request.Namespace, request.Name))

	// Fetch the PlatformAdmin instance
	platformAdmin := &iotv1alpha2.PlatformAdmin{}
	if err := r.Get(ctx, request.NamespacedName, platformAdmin); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		klog.Errorf(Format("Get PlatformAdmin %s/%s error %v", request.Namespace, request.Name, err))
		return reconcile.Result{}, err
	}

	platformAdminStatus := platformAdmin.Status.DeepCopy()
	isDeleted := false

	// Always issue a patch when exiting this function so changes to the
	// resource are patched back to the API server.
	defer func(isDeleted *bool) {
		if !*isDeleted {
			platformAdmin.Status = *platformAdminStatus

			if err := r.Status().Update(ctx, platformAdmin); err != nil {
				klog.Errorf(Format("Update the status of PlatformAdmin %s/%s failed", platformAdmin.Namespace, platformAdmin.Name))
				reterr = kerrors.NewAggregate([]error{reterr, err})
			}

			if reterr != nil {
				klog.ErrorS(reterr, Format("Reconcile PlatformAdmin %s/%s failed", platformAdmin.Namespace, platformAdmin.Name))
			}
		}
	}(&isDeleted)

	if platformAdmin.DeletionTimestamp != nil {
		isDeleted = true
		return r.reconcileDelete(ctx, platformAdmin)
	}

	return r.reconcileNormal(ctx, platformAdmin, platformAdminStatus)
}

func (r *ReconcilePlatformAdmin) reconcileDelete(ctx context.Context, platformAdmin *iotv1alpha2.PlatformAdmin) (reconcile.Result, error) {
	klog.V(4).Infof(Format("ReconcileDelete PlatformAdmin %s/%s", platformAdmin.Namespace, platformAdmin.Name))
	yas := &appsv1alpha1.YurtAppSet{}
	var desiredComponents []*config.Component
	if platformAdmin.Spec.Security {
		desiredComponents = r.Configration.SecurityComponents[platformAdmin.Spec.Version]
	} else {
		desiredComponents = r.Configration.NoSectyComponents[platformAdmin.Spec.Version]
	}

	additionalComponents, err := annotationToComponent(platformAdmin.Annotations)
	if err != nil {
		klog.Errorf(Format("annotationToComponent error %v", err))
		return reconcile.Result{}, err
	}
	desiredComponents = append(desiredComponents, additionalComponents...)

	//TODO: handle PlatformAdmin.Spec.Components

	for _, dc := range desiredComponents {
		if err := r.Get(
			ctx,
			types.NamespacedName{Namespace: platformAdmin.Namespace, Name: dc.Name},
			yas); err != nil {
			klog.V(4).ErrorS(err, Format("Get YurtAppSet %s/%s error", platformAdmin.Namespace, dc.Name))
			continue
		}

		oldYas := yas.DeepCopy()

		for i, pool := range yas.Spec.Topology.Pools {
			if pool.Name == platformAdmin.Spec.PoolName {
				yas.Spec.Topology.Pools[i] = yas.Spec.Topology.Pools[len(yas.Spec.Topology.Pools)-1]
				yas.Spec.Topology.Pools = yas.Spec.Topology.Pools[:len(yas.Spec.Topology.Pools)-1]
			}
		}
		if err := r.Client.Patch(ctx, yas, client.MergeFrom(oldYas)); err != nil {
			klog.V(4).ErrorS(err, Format("Patch YurtAppSet %s/%s error", platformAdmin.Namespace, dc.Name))
			return reconcile.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(platformAdmin, iotv1alpha2.PlatformAdminFinalizer)
	if err := r.Client.Update(ctx, platformAdmin); err != nil {
		klog.Errorf(Format("Update PlatformAdmin %s error %v", klog.KObj(platformAdmin), err))
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *ReconcilePlatformAdmin) reconcileNormal(ctx context.Context, platformAdmin *iotv1alpha2.PlatformAdmin, platformAdminStatus *iotv1alpha2.PlatformAdminStatus) (reconcile.Result, error) {
	klog.V(4).Infof(Format("ReconcileNormal PlatformAdmin %s/%s", platformAdmin.Namespace, platformAdmin.Name))
	controllerutil.AddFinalizer(platformAdmin, iotv1alpha2.PlatformAdminFinalizer)

	platformAdmin.Status.Initialized = true
	klog.V(4).Infof(Format("ReconcileConfigmap PlatformAdmin %s/%s", platformAdmin.Namespace, platformAdmin.Name))
	if ok, err := r.reconcileConfigmap(ctx, platformAdmin, platformAdminStatus); !ok {
		if err != nil {
			util.SetPlatformAdminCondition(platformAdminStatus, util.NewPlatformAdminCondition(iotv1alpha2.ConfigmapAvailableCondition, corev1.ConditionFalse, iotv1alpha2.ConfigmapProvisioningFailedReason, err.Error()))
			return reconcile.Result{}, errors.Wrapf(err,
				"unexpected error while reconciling configmap for %s", platformAdmin.Namespace+"/"+platformAdmin.Name)
		}
		util.SetPlatformAdminCondition(platformAdminStatus, util.NewPlatformAdminCondition(iotv1alpha2.ConfigmapAvailableCondition, corev1.ConditionFalse, iotv1alpha2.ConfigmapProvisioningReason, ""))
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}
	util.SetPlatformAdminCondition(platformAdminStatus, util.NewPlatformAdminCondition(iotv1alpha2.ConfigmapAvailableCondition, corev1.ConditionTrue, "", ""))

	klog.V(4).Infof(Format("ReconcileComponent PlatformAdmin %s/%s", platformAdmin.Namespace, platformAdmin.Name))
	if ok, err := r.reconcileComponent(ctx, platformAdmin, platformAdminStatus); !ok {
		if err != nil {
			util.SetPlatformAdminCondition(platformAdminStatus, util.NewPlatformAdminCondition(iotv1alpha2.ComponentAvailableCondition, corev1.ConditionFalse, iotv1alpha2.ComponentProvisioningReason, err.Error()))
			return reconcile.Result{}, errors.Wrapf(err,
				"unexpected error while reconciling component for %s", platformAdmin.Namespace+"/"+platformAdmin.Name)
		}
		util.SetPlatformAdminCondition(platformAdminStatus, util.NewPlatformAdminCondition(iotv1alpha2.ComponentAvailableCondition, corev1.ConditionFalse, iotv1alpha2.ComponentProvisioningReason, ""))
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}
	util.SetPlatformAdminCondition(platformAdminStatus, util.NewPlatformAdminCondition(iotv1alpha2.ComponentAvailableCondition, corev1.ConditionTrue, "", ""))

	platformAdminStatus.Ready = true
	if err := r.Client.Update(ctx, platformAdmin); err != nil {
		klog.Errorf(Format("Update PlatformAdmin %s error %v", klog.KObj(platformAdmin), err))
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *ReconcilePlatformAdmin) reconcileConfigmap(ctx context.Context, platformAdmin *iotv1alpha2.PlatformAdmin, _ *iotv1alpha2.PlatformAdminStatus) (bool, error) {
	var configmaps []corev1.ConfigMap
	needConfigMaps := make(map[string]struct{})

	if platformAdmin.Spec.Security {
		configmaps = r.Configration.SecurityConfigMaps[platformAdmin.Spec.Version]
	} else {
		configmaps = r.Configration.NoSectyConfigMaps[platformAdmin.Spec.Version]
	}
	for _, configmap := range configmaps {
		// Supplement runtime information
		configmap.Namespace = platformAdmin.Namespace
		configmap.Labels = make(map[string]string)
		configmap.Labels[iotv1alpha2.LabelPlatformAdminGenerate] = LabelConfigmap

		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, &configmap, func() error {
			return controllerutil.SetOwnerReference(platformAdmin, &configmap, (r.Scheme()))
		})
		if err != nil {
			return false, err
		}

		needConfigMaps[configmap.Name] = struct{}{}
	}

	configmaplist := &corev1.ConfigMapList{}
	if err := r.List(ctx, configmaplist, client.InNamespace(platformAdmin.Namespace), client.MatchingLabels{iotv1alpha2.LabelPlatformAdminGenerate: LabelConfigmap}); err == nil {
		for _, c := range configmaplist.Items {
			if _, ok := needConfigMaps[c.Name]; !ok {
				r.removeOwner(ctx, platformAdmin, &c)
			}
		}
	}

	return true, nil
}

func (r *ReconcilePlatformAdmin) reconcileComponent(ctx context.Context, platformAdmin *iotv1alpha2.PlatformAdmin, platformAdminStatus *iotv1alpha2.PlatformAdminStatus) (bool, error) {
	var desireComponents []*config.Component
	needComponents := make(map[string]struct{})
	var readyComponent int32 = 0

	if platformAdmin.Spec.Security {
		desireComponents = r.Configration.SecurityComponents[platformAdmin.Spec.Version]
	} else {
		desireComponents = r.Configration.NoSectyComponents[platformAdmin.Spec.Version]
	}

	additionalComponents, err := annotationToComponent(platformAdmin.Annotations)
	if err != nil {
		return false, err
	}
	desireComponents = append(desireComponents, additionalComponents...)

	//TODO: handle PlatformAdmin.Spec.Components

	defer func() {
		platformAdminStatus.ReadyComponentNum = readyComponent
		platformAdminStatus.UnreadyComponentNum = int32(len(desireComponents)) - readyComponent
	}()

NextC:
	for _, desireComponent := range desireComponents {
		readyService := false
		readyDeployment := false
		needComponents[desireComponent.Name] = struct{}{}

		if _, err := r.handleService(ctx, platformAdmin, desireComponent); err != nil {
			return false, err
		}
		readyService = true

		yas := &appsv1alpha1.YurtAppSet{}
		err := r.Get(
			ctx,
			types.NamespacedName{
				Namespace: platformAdmin.Namespace,
				Name:      desireComponent.Name},
			yas)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return false, err
			}
			_, err = r.handleYurtAppSet(ctx, platformAdmin, desireComponent)
			if err != nil {
				return false, err
			}
		} else {
			oldYas := yas.DeepCopy()

			if _, ok := yas.Status.PoolReplicas[platformAdmin.Spec.PoolName]; ok {
				if yas.Status.ReadyReplicas == yas.Status.Replicas {
					readyDeployment = true
					if readyDeployment && readyService {
						readyComponent++
					}
				}
				continue NextC
			}
			pool := appsv1alpha1.Pool{
				Name:     platformAdmin.Spec.PoolName,
				Replicas: pointer.Int32Ptr(1),
			}
			pool.NodeSelectorTerm.MatchExpressions = append(pool.NodeSelectorTerm.MatchExpressions,
				corev1.NodeSelectorRequirement{
					Key:      appsv1alpha1.LabelCurrentNodePool,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{platformAdmin.Spec.PoolName},
				})
			flag := false
			for _, up := range yas.Spec.Topology.Pools {
				if up.Name == pool.Name {
					flag = true
					break
				}
			}
			if !flag {
				yas.Spec.Topology.Pools = append(yas.Spec.Topology.Pools, pool)
			}
			if err := controllerutil.SetOwnerReference(platformAdmin, yas, r.Scheme()); err != nil {
				return false, err
			}
			if err := r.Client.Patch(ctx, yas, client.MergeFrom(oldYas)); err != nil {
				klog.Errorf(Format("Patch yurtappset %s/%s failed: %v", yas.Namespace, yas.Name, err))
				return false, err
			}
		}
	}

	// Remove the service owner that we do not need
	servicelist := &corev1.ServiceList{}
	if err := r.List(ctx, servicelist, client.InNamespace(platformAdmin.Namespace), client.MatchingLabels{iotv1alpha2.LabelPlatformAdminGenerate: LabelService}); err == nil {
		for _, s := range servicelist.Items {
			if _, ok := needComponents[s.Name]; !ok {
				r.removeOwner(ctx, platformAdmin, &s)
			}
		}
	}

	// Remove the yurtappset owner that we do not need
	yurtappsetlist := &appsv1alpha1.YurtAppSetList{}
	if err := r.List(ctx, yurtappsetlist, client.InNamespace(platformAdmin.Namespace), client.MatchingLabels{iotv1alpha2.LabelPlatformAdminGenerate: LabelDeployment}); err == nil {
		for _, s := range yurtappsetlist.Items {
			if _, ok := needComponents[s.Name]; !ok {
				r.removeOwner(ctx, platformAdmin, &s)
			}
		}
	}

	return readyComponent == int32(len(desireComponents)), nil
}

func (r *ReconcilePlatformAdmin) handleService(ctx context.Context, platformAdmin *iotv1alpha2.PlatformAdmin, component *config.Component) (*corev1.Service, error) {
	// It is possible that the component does not need service.
	// Therefore, you need to be careful when calling this function.
	// It is still possible for service to be nil when there is no error!
	if component.Service == nil {
		return nil, nil
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
			Name:        component.Name,
			Namespace:   platformAdmin.Namespace,
		},
		Spec: *component.Service,
	}
	service.Labels[iotv1alpha2.LabelPlatformAdminGenerate] = LabelService
	service.Annotations[AnnotationServiceTopologyKey] = AnnotationServiceTopologyValueNodePool

	_, err := controllerutil.CreateOrUpdate(
		ctx,
		r.Client,
		service,
		func() error {
			return controllerutil.SetOwnerReference(platformAdmin, service, r.Scheme())
		},
	)

	if err != nil {
		return nil, err
	}
	return service, nil
}

func (r *ReconcilePlatformAdmin) handleYurtAppSet(ctx context.Context, platformAdmin *iotv1alpha2.PlatformAdmin, component *config.Component) (*appsv1alpha1.YurtAppSet, error) {
	yas := &appsv1alpha1.YurtAppSet{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
			Name:        component.Name,
			Namespace:   platformAdmin.Namespace,
		},
		Spec: appsv1alpha1.YurtAppSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": component.Name},
			},
			WorkloadTemplate: appsv1alpha1.WorkloadTemplate{
				DeploymentTemplate: &appsv1alpha1.DeploymentTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": component.Name},
					},
					Spec: *component.Deployment,
				},
			},
		},
	}

	yas.Labels[iotv1alpha2.LabelPlatformAdminGenerate] = LabelDeployment
	pool := appsv1alpha1.Pool{
		Name:     platformAdmin.Spec.PoolName,
		Replicas: pointer.Int32Ptr(1),
	}
	pool.NodeSelectorTerm.MatchExpressions = append(pool.NodeSelectorTerm.MatchExpressions,
		corev1.NodeSelectorRequirement{
			Key:      appsv1alpha1.LabelCurrentNodePool,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{platformAdmin.Spec.PoolName},
		})
	yas.Spec.Topology.Pools = append(yas.Spec.Topology.Pools, pool)
	if err := controllerutil.SetControllerReference(platformAdmin, yas, r.Scheme()); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, yas); err != nil {
		return nil, err
	}
	return yas, nil
}

func (r *ReconcilePlatformAdmin) removeOwner(ctx context.Context, platformAdmin *iotv1alpha2.PlatformAdmin, obj client.Object) error {
	owners := obj.GetOwnerReferences()

	for i, owner := range owners {
		if owner.UID == platformAdmin.UID {
			owners[i] = owners[len(owners)-1]
			owners = owners[:len(owners)-1]

			if len(owners) == 0 {
				return r.Delete(ctx, obj)
			} else {
				obj.SetOwnerReferences(owners)
				return r.Update(ctx, obj)
			}
		}
	}
	return nil
}

// For version compatibility, v1alpha1's additionalservice and additionaldeployment are placed in
// v2alpha2's annotation, this function is to convert the annotation to component.
func annotationToComponent(annotation map[string]string) ([]*config.Component, error) {
	var components []*config.Component = []*config.Component{}
	var additionalDeployments []iotv1alpha1.DeploymentTemplateSpec = make([]iotv1alpha1.DeploymentTemplateSpec, 0)
	if _, ok := annotation["AdditionalDeployments"]; ok {
		err := json.Unmarshal([]byte(annotation["AdditionalDeployments"]), &additionalDeployments)
		if err != nil {
			return nil, err
		}
	}
	var additionalServices []iotv1alpha1.ServiceTemplateSpec = make([]iotv1alpha1.ServiceTemplateSpec, 0)
	if _, ok := annotation["AdditionalServices"]; ok {
		err := json.Unmarshal([]byte(annotation["AdditionalServices"]), &additionalServices)
		if err != nil {
			return nil, err
		}
	}
	if len(additionalDeployments) == 0 && len(additionalServices) == 0 {
		return components, nil
	}
	var services map[string]*corev1.ServiceSpec = make(map[string]*corev1.ServiceSpec)
	var usedServices map[string]struct{} = make(map[string]struct{})
	for _, additionalservice := range additionalServices {
		services[additionalservice.Name] = &additionalservice.Spec
	}
	for _, additionalDeployment := range additionalDeployments {
		var component config.Component
		component.Name = additionalDeployment.Name
		component.Deployment = &additionalDeployment.Spec
		service, ok := services[component.Name]
		if ok {
			component.Service = service
			usedServices[component.Name] = struct{}{}
		}
		components = append(components, &component)
	}
	if len(usedServices) < len(services) {
		for name, service := range services {
			_, ok := usedServices[name]
			if ok {
				continue
			}
			var component config.Component
			component.Name = name
			component.Service = service
			components = append(components, &component)
		}
	}

	return components, nil
}
