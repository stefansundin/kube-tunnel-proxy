package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type Tunnel struct {
	Namespace string
	Selector  string
	PodPort   int
	LocalPort int
}

type Logger struct {
	Context string
	Tag     string
}

func (this *Logger) Write(b []byte) (int, error) {
	fmt.Printf("[%s] Logger: %s, %s", this.Context, this.Tag, string(b))
	return 0, nil
}

func main() {
	config := map[string][]Tunnel{
		"minikube": {
			{
				Namespace: "kube-system",
				Selector:  "app=kubernetes-dashboard",
				PodPort:   9090,
				LocalPort: 8000,
			},
		},
	}

	var wg sync.WaitGroup
	for context, tunnels := range config {
		fmt.Printf("[%s] Setting up %d tunnels.\n", context, len(tunnels))

		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{
				CurrentContext: context,
			}).ClientConfig()

		if err != nil {
			panic(err.Error())
		}

		clientSet, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			panic(err.Error())
		}

		for _, tunnel := range tunnels {
			wg.Add(1)
			go PortForward(&wg, cfg, clientSet, &context, &tunnel)
		}
	}
	wg.Wait()
}

func PortForward(wg *sync.WaitGroup, cfg *rest.Config, clientSet *kubernetes.Clientset, context *string, tunnel *Tunnel) {
	defer wg.Done()

	pods, err := clientSet.CoreV1().
		Pods(tunnel.Namespace).
		List(metav1.ListOptions{
			LabelSelector: tunnel.Selector,
		})
	if err != nil {
		panic(err.Error())
	}
	if len(pods.Items) < 1 {
		fmt.Printf("[%s] No pods found: %s.\n", *context, tunnel.Selector)
		return
	}
	podName := pods.Items[0].Name

	fmt.Printf("[%s] Forwarding localhost:%d to pod %s:%d\n", *context, tunnel.LocalPort, podName, tunnel.PodPort)

	stopChan := make(chan struct{}, 1)
	readyChan := make(chan struct{})

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)

	go func() {
		<-signals
		if stopChan != nil {
			fmt.Printf("[%s] Stopped forwarding %s:%d.\n", *context, podName, tunnel.PodPort)
			close(stopChan)
		}
	}()

	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
		os.Exit(1)
	}

	restClient := clientSet.RESTClient()
	req := restClient.Post().
		Resource("pods").
		Namespace(tunnel.Namespace).
		Name(podName).
		SubResource("portforward")

	dialer := spdy.NewDialer(upgrader, &http.Client{
		Transport: transport,
	}, "POST", &url.URL{
		Scheme:   req.URL().Scheme,
		Host:     req.URL().Host,
		Path:     "/api/v1" + req.URL().Path,
		RawQuery: "timeout=10s",
	})

	ports := []string{
		fmt.Sprintf("%d:%d", tunnel.LocalPort, tunnel.PodPort),
	}
	logger := &Logger{
		Context: *context,
		Tag:     fmt.Sprintf("%s:%d", podName, tunnel.LocalPort),
	}

	fw, err := portforward.New(dialer, ports, stopChan, readyChan, logger, logger)
	if err != nil {
		panic(err.Error())
	}

	err = fw.ForwardPorts()
	if err != nil {
		panic(err.Error())
	}
}
