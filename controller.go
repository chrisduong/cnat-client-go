/*
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

package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	clientset "github.com/chrisduong/cnat-client-go/pkg/generated/clientset/versioned"
	cnatscheme "github.com/chrisduong/cnat-client-go/pkg/generated/clientset/versioned/scheme"
	informers "github.com/chrisduong/cnat-client-go/pkg/generated/informers/externalversions/cnat/v1alpha1"
	listers "github.com/chrisduong/cnat-client-go/pkg/generated/listers/cnat/v1alpha1"
	cnatv1alpha1 "github.com/programming-kubernetes/cnat/cnat-client-go/pkg/apis/cnat/v1alpha1"
	corev1informer "k8s.io/client-go/informers/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
)

const controllerAgentName = "cnat-controller"

// Controller is the controller implementation for Foo resources
type Controller struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// cnatclientset is a clientset for our own API group
	cnatclientset clientset.Interface

	atsLister listers.AtLister
	atsSynced cache.InformerSynced

	podsLister corev1lister.PodLister
	podsSynced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
}

// NewController returns a new sample controller
func NewController(
	kubeclientset kubernetes.Interface,
	cnatclientset clientset.Interface,
	atInformer informers.AtInformer,
	podInformer corev1informer.PodInformer) *Controller {

	// Create event broadcaster
	// Add cnat-controller types to the default Kubernetes Scheme so Events can be
	// logged for cnat-controller types.
	utilruntime.Must(cnatscheme.AddToScheme(scheme.Scheme))
	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &Controller{
		kubeclientset: kubeclientset,
		cnatclientset: cnatclientset,
		atsLister:     atInformer.Lister(),
		atsSynced:     atInformer.Informer().HasSynced,
		podsLister:    podInformer.Lister(),
		podsSynced:    podInformer.Informer().HasSynced,
		workqueue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Ats"),
		recorder:      recorder,
	}

	klog.Info("Setting up event handlers")
	// Set up an event handler for when Foo resources change
	atInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueFoo,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueFoo(new)
		},
	})
	// Set up an event handler for when Pod resources change
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueuePod,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueuePod(new)
		},
	})

	return controller
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting Foo controller")

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.deploymentsSynced, c.atsSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch two workers to process Foo resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// At resource to be synced.
		if when, err := c.syncHandler(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		} else if when != time.Duration(0) {
			c.workqueue.AddAfter(key, when)
		} else {
			// Finally, if no error occurs we Forget this item so it does not
			// get queued again until another change happens.
			c.workqueue.Forget(obj)
		}
		klog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the At resource
// with the current status of the resource.
// It returns how long to wait until the schedule is due.
func (c *Controller) syncHandler(key string) (time.Duration, error) {
	klog.Infof("=== Reconciling At %s", key)

	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return time.Duration(0), nil
	}

	// Get the At resource with this namespace/name
	original, err := c.atsLister.Ats(namespace).Get(name)
	if err != nil {
		// The At resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("at '%s' in work queue no longer exists", key))
			return time.Duration(0), nil
		}

		return time.Duration(0), err
	}

	// Clone because the original object is owned by the lister.
	instance := original.DeepCopy()

	// If no phase set, default to pending (the initial phase):
	if instance.Status.Phase == "" {
		instance.Status.Phase = cnatv1alpha1.PhasePending
	}

	// Now let's make the main case distinction: implementing
	// the state diagram PENDING -> RUNNING -> DONE
	switch instance.Status.Phase {
	case cnatv1alpha1.PhasePending:
		klog.Infof("instance %s: phase=PENDING", key)
		// As long as we haven't executed the command yet,  we need to check if it's time already to act:
		klog.Infof("instance %s: checking schedule %q", key, instance.Spec.Schedule)
		// Check if it's already time to execute the command with a tolerance of 2 seconds:
		d, err := timeUntilSchedule(instance.Spec.Schedule)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("schedule parsing failed: %v", err))
			// Error reading the schedule - requeue the request:
			return time.Duration(0), err
		}
		klog.Infof("instance %s: schedule parsing done: diff=%v", key, d)
		if d > 0 {
			// Not yet time to execute the command, wait until the scheduled time
			return d, nil
		}

		klog.Infof("instance %s: it's time! Ready to execute: %s", key, instance.Spec.Command)
		instance.Status.Phase = cnatv1alpha1.PhaseRunning
	case cnatv1alpha1.PhaseRunning:
		klog.Infof("instance %s: Phase: RUNNING", key)

		pod := newPodForCR(instance)

		// Set At instance as the owner and controller
		owner := metav1.NewControllerRef(instance, cnatv1alpha1.SchemeGroupVersion.WithKind("At"))
		pod.ObjectMeta.OwnerReferences = append(pod.ObjectMeta.OwnerReferences, *owner)

		// Try to see if the pod already exists and if not
		// (which we expect) then create a one-shot pod as per spec:
		found, err := c.kubeClientset.CoreV1().Pods(pod.Namespace).Get(pod.Name, metav1.GetOptions{})
		if err != nil && errors.IsNotFound(err) {
			found, err = c.kubeClientset.CoreV1().Pods(pod.Namespace).Create(pod)
			if err != nil {
				return time.Duration(0), err
			}
			klog.Infof("instance %s: pod launched: name=%s", key, pod.Name)
		} else if err != nil {
			// requeue with error
			return time.Duration(0), err
		} else if found.Status.Phase == corev1.PodFailed || found.Status.Phase == corev1.PodSucceeded {
			klog.Infof("instance %s: container terminated: reason=%q message=%q", key, found.Status.Reason, found.Status.Message)
			instance.Status.Phase = cnatv1alpha1.PhaseDone
		} else {
			// don't requeue because it will happen automatically when the pod status changes
			return time.Duration(0), nil
		}
	case cnatv1alpha1.PhaseDone:
		klog.Infof("instance %s: phase: DONE", key)
		return time.Duration(0), nil
	default:
		klog.Infof("instance %s: NOP", key)
		return time.Duration(0), nil
	}

	if !reflect.DeepEqual(original, instance) {
		// Update the At instance, setting the status to the respective phase:
		_, err = c.cnatClientset.CnatV1alpha1().Ats(instance.Namespace).UpdateStatus(instance)
		if err != nil {
			return time.Duration(0), err
		}
	}

	// Don't requeue. We should be reconcile because either the pod or the CR changes.
	return time.Duration(0), nil
}

func (c *Controller) updateFooStatus(foo *samplev1alpha1.Foo, deployment *appsv1.Deployment) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	// You can use DeepCopy() to make a deep copy of original object and modify this copy
	// Or create a copy manually for better performance
	fooCopy := foo.DeepCopy()
	fooCopy.Status.AvailableReplicas = deployment.Status.AvailableReplicas
	// If the CustomResourceSubresources feature gate is not enabled,
	// we must use Update instead of UpdateStatus to update the Status block of the Foo resource.
	// UpdateStatus will not allow changes to the Spec of the resource,
	// which is ideal for ensuring nothing other than resource status has been updated.
	_, err := c.cnatclientset.cnatV1alpha1().Foos(foo.Namespace).Update(context.TODO(), fooCopy, metav1.UpdateOptions{})
	return err
}

// enqueueFoo takes a Foo resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than Foo.
func (c *Controller) enqueueFoo(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(key)
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the Foo resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that Foo resource to be processed. If the object does not
// have an appropriate OwnerReference, it will simply be skipped.
func (c *Controller) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
		klog.V(4).Infof("Recovered deleted object '%s' from tombstone", object.GetName())
	}
	klog.V(4).Infof("Processing object: %s", object.GetName())
	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		// If this object is not owned by a Foo, we should not do anything more
		// with it.
		if ownerRef.Kind != "Foo" {
			return
		}

		foo, err := c.atsLister.Foos(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			klog.V(4).Infof("ignoring orphaned object '%s' of foo '%s'", object.GetSelfLink(), ownerRef.Name)
			return
		}

		c.enqueueFoo(foo)
		return
	}
}

// newPodForCR returns a busybox pod with the same name/namespace as the cr
func newPodForCR(cr *cnatv1alpha1.At) *corev1.Pod {
	labels := map[string]string{
		"app": cr.Name,
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-pod",
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "busybox",
					Image:   "busybox",
					Command: strings.Split(cr.Spec.Command, " "),
				},
			},
			RestartPolicy: corev1.RestartPolicyOnFailure,
		},
	}
}

// timeUntilSchedule parses the schedule string and returns the time until the schedule.
// When it is overdue, the duration is negative.
func timeUntilSchedule(schedule string) (time.Duration, error) {
	now := time.Now().UTC()
	layout := "2006-01-02T15:04:05Z"
	s, err := time.Parse(layout, schedule)
	if err != nil {
		return time.Duration(0), err
	}
	return s.Sub(now), nil
}
