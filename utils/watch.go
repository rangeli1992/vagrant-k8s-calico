package utils

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	"k8s.io/klog/v2"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
)

const (
	// CreateEvent event associated with new objects in an informer
	CreateEvent = "CREATE"
	// UpdateEvent event associated with an object update in an informer
	UpdateEvent = "UPDATE"
	// DeleteEvent event associated when an object is removed from an informer
	DeleteEvent = "DELETE"
)

type Controller struct {
	indexer  cache.Indexer
	queue    workqueue.RateLimitingInterface
	informer cache.Controller
	f        *os.File
}

type Event struct {
	Type string
	Obj  *v1.Event
	Key  string
}

func NewController(queue workqueue.RateLimitingInterface, indexer cache.Indexer, informer cache.Controller, f *os.File) *Controller {
	return &Controller{
		informer: informer,
		indexer:  indexer,
		queue:    queue,
		f:		  f,
	}
}

func (c *Controller) processNextItem() bool {
	// Wait until there is a new item in the working queue
	newEvent, quit := c.queue.Get()
	if quit {
		return false
	}
	// Tell the queue that we are done with processing this key. This unblocks the key for other workers
	// This allows safe parallel processing because two pods with the same key are never processed in
	// parallel.
	defer c.queue.Done(newEvent)

	// Invoke the method containing the business logic
	err := c.syncToStdout(newEvent.(string))
	// Handle the error if something went wrong during the execution of the business logic
	c.handleErr(err, newEvent)
	return true
}

// syncToStdout is the business logic of the controller. In this controller it simply prints
// information about the pod to stdout. In case an error happened, it has to simply return the error.
// The retry logic should not be part of the business logic.
func (c *Controller) syncToStdout(key string) error {
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		klog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		// Below we will warm up our cache with a Pod, so that we will see a delete for one pod
		fmt.Printf("Pod %s does not exist anymore\n", key)
		//io.WriteString(c.f, fmt.Sprintf("Delete %s \n", key))
	} else {
		// Note that you also have to check the uid if you have a local controlled resource, which
		// is dependent on the actual instance, to detect that a Pod was recreated with the same name
		evt := obj.(*v1.Event)
		msg := fmt.Sprintf("%s: %s/%s/%s/%s \n", evt.Name, evt.Namespace, evt.Kind, evt.Name, evt.Message)
		fmt.Printf(msg)
		_, err := io.WriteString(c.f, msg)
		if err != nil {
			fmt.Printf("write file error %s", err.Error())
		}
	}
	return nil
}

// handleErr checks if an error happened and makes sure we will retry later.
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		c.queue.Forget(key)
		return
	}

	// This controller retries 5 times if something goes wrong. After that, it stops trying.
	if c.queue.NumRequeues(key) < 5 {
		klog.Infof("Error syncing pod %v: %v", key, err)

		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// queue and the re-enqueue history, the key will be processed later again.
		c.queue.AddRateLimited(key)
		return
	}

	c.queue.Forget(key)
	// Report to an external entity that, even after several retries, we could not successfully process this key
	runtime.HandleError(err)
	klog.Infof("Dropping pod %q out of the queue: %v", key, err)
}

func (c *Controller) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()

	// Let the workers stop when we are done
	defer c.queue.ShutDown()
	klog.Info("Starting Pod controller")

	go c.informer.Run(stopCh)

	// Wait for all involved caches to be synced, before processing items from the queue is started
	//if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
	//	runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync "))
	//	return
	//}

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	klog.Info("Stopping Pod controller")
}

func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

func Watch(kubeconfig *string) {
	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatal(err)
	}

	// create the pod watcher
	podListWatcher := cache.NewListWatchFromClient(clientset.CoreV1().RESTClient(), "events", v1.NamespaceAll, fields.Everything())

	// create the workqueue
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	var newEvent Event

	// Bind the workqueue to a cache with the help of an informer. This way we make sure that
	// whenever the cache is updated, the pod key is added to the workqueue.
	// Note that when we finally process the item from the workqueue, we might see a newer version
	// of the Pod than the version which was responsible for triggering the update.
	indexer, informer := cache.NewIndexerInformer(podListWatcher, &v1.Event{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			evt := obj.(*v1.Event)
			newEvent.Obj = evt
			newEvent.Type = CreateEvent
			newEvent.Key, err = cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(newEvent.Key)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			oldEvt := old.(*v1.Event)
			newEvt := new.(*v1.Event)
			if !reflect.DeepEqual(newEvt.Source, oldEvt.Source) && oldEvt.Reason != newEvt.Reason {
				newEvent.Obj = newEvt
				newEvent.Type = UpdateEvent
				newEvent.Key, err = cache.MetaNamespaceKeyFunc(old)
				if err == nil {
					queue.Add(newEvent.Key)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			evt := obj.(*v1.Event)
			newEvent.Obj = evt
			newEvent.Type = DeleteEvent
			newEvent.Key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(newEvent.Key)
			}
		},
	}, cache.Indexers{})

	// file
	var filename = "./watch.txt"
	var f *os.File
	var err1 error
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		f, err1 = os.Create(filename)
	} else {
		f, err1 = os.OpenFile(filename, os.O_APPEND | os.O_RDWR, 0666)
	}
	if err1 != nil {
		panic(err1)
	}

	defer f.Close()

	controller := NewController(queue, indexer, informer, f)

	// Now let's start the controller
	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)

	// Wait forever
	select {}
}
