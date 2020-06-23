package main

import (
	"bufio"
	"entryTask/utils"
	"flag"
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/util/retry"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	var kubeconfig *string
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// listen event
	go utils.Watch(kubeconfig)

	// 1 create namespace
	name := "entry-task"
	namespacesClient := clientset.CoreV1().Namespaces()
	namespace := &apiv1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: apiv1.NamespaceStatus{
			Phase: apiv1.NamespaceActive,
		},
	}
	fmt.Println("Creating Namespaces...")
	result, err := namespacesClient.Create(namespace)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created Namespaces %s on %s\n", result.ObjectMeta.Name, result.ObjectMeta.CreationTimestamp)

	// 2 create deployment
	deploymentName := "nginx-deployment"
	deploymentsClient := clientset.AppsV1().Deployments(name)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: deploymentName,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(2),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "demo",
				},
			},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "demo",
					},
				},
				Spec: apiv1.PodSpec{
					Containers: []apiv1.Container{
						{
							Name:  "web",
							Image: "nginx:1.12",
							Ports: []apiv1.ContainerPort{
								{
									Name:          "http",
									Protocol:      apiv1.ProtocolTCP,
									ContainerPort: 80,
								},
							},
						},
					},
				},
			},
		},
	}

	fmt.Println("Creating deployment...")
	deploymentResult, err := deploymentsClient.Create(deployment)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Created deployment %q.\n", deploymentResult.GetObjectMeta().GetName())

	// 3 update deployment
	prompt()
	fmt.Println("Updating deployment...")

	cpuLimit := "700m"
	memLimit := "200Mi"
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		r, getErr := deploymentsClient.Get(deploymentName, metav1.GetOptions{})
		if getErr != nil {
			panic(fmt.Errorf("Failed to get latest version of Deployment: %v ", getErr))
		}

		r.Spec.Template.Spec.Containers[0].Image = "nginx:latest" // change nginx version
		r.Spec.Template.Spec.Containers[0].Resources = apiv1.ResourceRequirements {
			Limits: apiv1.ResourceList{
				apiv1.ResourceCPU: resource.MustParse(cpuLimit),
				apiv1.ResourceMemory: resource.MustParse(memLimit),
			},
			Requests: apiv1.ResourceList{
				apiv1.ResourceCPU: resource.MustParse(cpuLimit),
				apiv1.ResourceMemory: resource.MustParse(memLimit),
			},
		}
		_, updateErr := deploymentsClient.Update(r)
		return updateErr
	})
	if retryErr != nil {
		panic(fmt.Errorf("Update failed: %v ", retryErr))
	}
	fmt.Println("Updated deployment...")

	// List Deployments
	prompt()
	fmt.Printf("Listing deployments in namespace %q:\n", name)
	list, err := deploymentsClient.List(metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	for _, d := range list.Items {
		fmt.Printf(" * %s (%d replicas)\n", d.Name, *d.Spec.Replicas)
	}

	// Delete Deployment
	prompt()
	fmt.Println("Deleting deployment...")
	deletePolicy := metav1.DeletePropagationForeground
	if err := deploymentsClient.Delete(deploymentName, &metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}); err != nil {
		panic(err)
	}
	fmt.Println("Deleted deployment.")

	// Delete Namespace
	fmt.Println("Deleting Namespaces...")
	deletePolicy = metav1.DeletePropagationForeground
	if err := namespacesClient.Delete(name, &metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}); err != nil {
		panic(err)
	}
	fmt.Printf("Deleted Namespaces %s\n", name)

	for {
		pods, err := clientset.CoreV1().Pods("").List(metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}
		fmt.Printf("There are %d pods in the cluster\n", len(pods.Items))

		time.Sleep(10 * time.Second)
	}
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func prompt() {
	fmt.Printf("-> Press Return key to continue.\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		break
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}
	fmt.Println()
}

func int32Ptr(i int32) *int32 { return &i }